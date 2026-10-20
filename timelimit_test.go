package jaml

import (
	"context"
	"errors"
	"strings"
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
		expectError(t, err, InvalidInput, "$: a time limit must be positive, got 0s")
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

func TestOnlyAWedgedChildPastAnOverseenLimitTurnsIntoTimeLimit(t *testing.T) {
	overdue, stop := context.WithCancelCause(t.Context())
	stop(_TimeLimit(time.Millisecond).exhausted())
	var answered = _Answered{data: []byte("5")}
	var silent = _Lost{cause: errSilent}
	var unread = _Lost{cause: errNotReading}
	var outside = _Lost{cause: _OutsideDeath("killed from outside")}
	var missing = _Refused{err: errMissing}
	var thrown = _Refused{err: _Failure{Kind: "EvaluationFailed", At: "$.v", Message: "Error: boom"}.rebuild()}
	for _, result := range []_Outcome{answered, silent, unread, outside, missing, thrown} {
		expectEqual(t, overran(t.Context(), result), result)
	}
	for _, result := range []_Outcome{answered, outside, missing, thrown} {
		expectEqual(t, overran(overdue, result), result)
	}
	expectError(t, located("$.v", refusal(t, overran(overdue, silent))), TimeLimit, "$.v: exceeded the time limit of 1ms: the evaluator stopped answering")
	expectError(t, located("$.v", refusal(t, overran(overdue, unread))), TimeLimit, "$.v: exceeded the time limit of 1ms: the evaluator was not reading requests")
}

func refusal(t *testing.T, result _Outcome) error {
	t.Helper()
	refused, isRefused := result.(_Refused)
	if !isRefused {
		t.Fatalf("expected a refusal, got %#v", result)
	}
	return refused.err
}

func TestWaitingBehindABigRequestNeverCountsTowardALimit(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(1<<30), WithMaxConcurrency(4))
	var big = mustJaml(t, sb, "a: ${ big.length }\n")
	var small = mustJaml(t, sb, "b: ${ 1 }\nspin: ${ (() => { while (true) {} })() }\n")
	var child = currentChild(t, sb)
	var payload = strings.Repeat("x", 128<<20)
	var carried = make(chan error, 1)
	go func() {
		var out int
		carried <- resolveInto(WithValues(t.Context(), map[string]any{"big": payload}), &out, big.At("a"))
	}()
	for len(child.wire.writer) == 0 {
		time.Sleep(time.Millisecond)
	}
	var queued = time.Now()
	var spun = make(chan error, 1)
	go func() {
		var out any
		spun <- resolveInto(WithTimeLimit(t.Context(), 100*time.Millisecond), &out, small.At("spin"))
	}()
	expectEqual(t, mustResolve[int](t, WithTimeLimit(t.Context(), 100*time.Millisecond), small.At("b")), 1)
	if waited := time.Since(queued); waited < 100*time.Millisecond+cancelGrace {
		t.Logf("the big request was written within %s, so the limited call barely waited", waited)
	}
	expectError(t, <-spun, TimeLimit, "$.spin: ${(() => { while (true) {} })()}: exceeded the time limit of 100ms")
	if err := <-carried; err != nil {
		t.Fatalf("the big request failed: %v", err)
	}
	if !child.alive() {
		t.Fatalf("the evaluator died under queued work: %v", child.lost().cause)
	}
}
