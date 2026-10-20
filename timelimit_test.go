package jaml

import (
	"context"
	"errors"
	"testing"
	"time"
)

const spinning = "spin: ${ (() => { while (true) {} })() }\nquick: ${ 1 + 1 }\n"

func TestARunPastItsTimeLimitFailsWithTimeLimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var started = time.Now()
		var out any
		var err = resolveInto(WithTimeLimit(t.Context(), 100*time.Millisecond), &out, document.At("spin"))
		expectError(t, err, TimeLimit, "$.spin: ${(() => { while (true) {} })()}: exceeded the time limit of 100ms: evaluation failed: expression error")
		expectError(t, err, EvaluationFailed)
		expectFailure(t, err, "$.spin", "exceeded the time limit of 100ms")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("a time limit looked like the caller's context: %v", err)
		}
		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("a 100ms time limit held its caller for %s", waited)
		}
		expectEqual(t, mustResolve[int](t, WithTimeLimit(t.Context(), time.Second), document.At("quick")), 2)
	})
}

func TestTheCallersContextStillBoundsATimeLimitedRun(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithTimeLimit(within(t, 100*time.Millisecond), time.Minute), &out, mustJaml(t, sb, spinning).At("spin"))
		expectError(t, err, EvaluationFailed, "deadline exceeded")
		if errors.Is(err, TimeLimit) {
			t.Fatalf("the caller's deadline was taken for a time limit: %v", err)
		}
	})
}

func TestTheSmallestTimeLimitWins(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var out any
		var inner = WithTimeLimit(WithTimeLimit(t.Context(), 50*time.Millisecond), time.Minute)
		expectError(t, resolveInto(inner, &out, document.At("spin")), TimeLimit, "exceeded the time limit of 50ms")
		var outer = WithTimeLimit(WithTimeLimit(t.Context(), time.Minute), 60*time.Millisecond)
		expectError(t, resolveInto(outer, &out, document.At("spin")), TimeLimit, "exceeded the time limit of 60ms")
	})
}

func TestANonPositiveTimeLimitIsRefused(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithTimeLimit(t.Context(), 0), &out, mustJaml(t, sb, spinning).At("quick"))
		expectError(t, err, InvalidExpression, "$: a time limit must be positive, got 0s")
	})
}

func TestWaitingForASlotDoesNotCountTowardTheTimeLimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var blocker = make(chan error, 1)
		go func() {
			var out any
			blocker <- resolveInto(within(t, 400*time.Millisecond), &out, document.At("spin"))
		}()
		time.Sleep(100 * time.Millisecond)
		var started = time.Now()
		expectEqual(t, mustResolve[int](t, WithTimeLimit(t.Context(), 50*time.Millisecond), document.At("quick")), 2)
		if waited := time.Since(started); waited < 200*time.Millisecond {
			t.Fatalf("the quick call returned after %s, so it never waited behind the spin", waited)
		}
		expectError(t, <-blocker, EvaluationFailed, "deadline exceeded")
	}, WithMaxConcurrency(1))
}

func TestOnlyAnOverseenLimitTurnsAFailedJobIntoTimeLimit(t *testing.T) {
	overdue, stop := context.WithCancelCause(t.Context())
	stop(_TimeLimit(time.Millisecond).exhausted())
	var answered = _Answered{data: []byte("5")}
	var lost = _Lost{cause: errSilent}
	var missing = _Refused{err: errMissing}
	var stuck = _Refused{err: _Failure{Kind: "EvaluationFailed", At: "$.v", Overran: true, Message: "exceeded the time limit of 1ms"}.rebuild()}
	var echo = _Refused{err: _Failure{Kind: "EvaluationFailed", At: "$.v", Stopped: true, Message: "cancelled: context canceled"}.rebuild()}
	var thrown = _Refused{err: _Failure{Kind: "EvaluationFailed", At: "$.v", Message: "Error: boom", Thrown: &_ThrownOnWire{Message: "boom", Value: _Borrowed(`"boom"`)}}.rebuild()}
	for _, result := range []_Outcome{answered, lost, missing, stuck, echo, thrown} {
		expectEqual(t, overran(t.Context(), result), result)
	}
	for _, result := range []_Outcome{answered, missing, stuck} {
		expectEqual(t, overran(overdue, result), result)
	}
	expectError(t, located("$.v", refusal(t, overran(overdue, lost))), TimeLimit, "$.v: exceeded the time limit of 1ms: the evaluator stopped answering")
	var dropped = refusal(t, overran(overdue, echo))
	expectEqual(t, dropped, error(_TimeExhausted{limit: time.Millisecond}))
	var kept = refusal(t, overran(overdue, thrown))
	expectError(t, kept, TimeLimit, "$.v: exceeded the time limit of 1ms: Error: boom")
	var failure *Failure
	if !errors.As(kept, &failure) || *failure != (Failure{Path: "$.v", Reason: "exceeded the time limit of 1ms: Error: boom"}) {
		t.Fatalf("a late failure past its limit reads %+v", failure)
	}
	var cause *Thrown
	if !errors.As(kept, &cause) || cause.Message != "boom" || cause.Value != "boom" {
		t.Fatalf("a late failure past its limit lost what was thrown: %+v", cause)
	}
}

func refusal(t *testing.T, result _Outcome) error {
	t.Helper()
	refused, isRefused := result.(_Refused)
	if !isRefused {
		t.Fatalf("expected a refusal, got %#v", result)
	}
	return refused.err
}
