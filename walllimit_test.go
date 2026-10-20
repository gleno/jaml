package jaml

import (
	"context"
	"errors"
	"fmt"
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
		var err = resolveInto(WithWallLimit(t.Context(), 100*time.Millisecond), &out, document.At("spin"))
		expectError(t, err, WallLimit, "$.spin: ${(() => { while (true) {} })()}: exceeded the wall time limit of 100ms: evaluation failed: expression error")
		expectError(t, err, EvaluationFailed)
		expectFailure(t, err, "$.spin", "exceeded the wall time limit of 100ms")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("a time limit looked like the caller's context: %v", err)
		}
		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("a 100ms time limit held its caller for %s", waited)
		}
		expectEqual(t, mustResolve[int](t, WithWallLimit(t.Context(), time.Second), document.At("quick")), 2)
	})
}

func TestTheCallersContextStillBoundsATimeLimitedRun(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithWallLimit(within(t, 100*time.Millisecond), time.Minute), &out, mustJaml(t, sb, spinning).At("spin"))
		expectError(t, err, EvaluationFailed, "deadline exceeded")
		if errors.Is(err, WallLimit) {
			t.Fatalf("the caller's deadline was taken for a time limit: %v", err)
		}
	})
}

func TestTheSmallestTimeLimitWins(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var out any
		var inner = WithWallLimit(WithWallLimit(t.Context(), 50*time.Millisecond), time.Minute)
		expectError(t, resolveInto(inner, &out, document.At("spin")), WallLimit, "exceeded the wall time limit of 50ms")
		var outer = WithWallLimit(WithWallLimit(t.Context(), time.Minute), 60*time.Millisecond)
		expectError(t, resolveInto(outer, &out, document.At("spin")), WallLimit, "exceeded the wall time limit of 60ms")
	})
}

func TestANonPositiveTimeLimitIsRefused(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithWallLimit(t.Context(), 0), &out, mustJaml(t, sb, spinning).At("quick"))
		expectError(t, err, InvalidInput, "$: a wall time limit must be positive, got 0s")
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
		expectEqual(t, mustResolve[int](t, WithWallLimit(t.Context(), 50*time.Millisecond), document.At("quick")), 2)
		if waited := time.Since(started); waited < 200*time.Millisecond {
			t.Fatalf("the quick call returned after %s, so it never waited behind the spin", waited)
		}
		expectError(t, <-blocker, EvaluationFailed, "deadline exceeded")
	}, WithMaxConcurrency(1))
}

func TestOnlyAWedgedChildPastAnOverseenLimitTurnsIntoTimeLimit(t *testing.T) {
	overdue, stop := context.WithCancelCause(t.Context())
	stop(_WallLimit(time.Millisecond).exhausted())
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
	expectError(t, located("$.v", refusal(t, overran(overdue, silent))), WallLimit, "$.v: exceeded the wall time limit of 1ms: the evaluator stopped answering")
	expectError(t, located("$.v", refusal(t, overran(overdue, unread))), WallLimit, "$.v: exceeded the wall time limit of 1ms: the evaluator was not reading requests")
}

func TestATimedOutJobBlamesNoOtherJobsDeath(t *testing.T) {
	overdue, stop := context.WithCancelCause(t.Context())
	stop(_WallLimit(time.Millisecond).exhausted())
	var errand = &_Errand{ctx: t.Context()}
	var starved = _Lost{cause: _MemoryExhausted("exceeded the memory limit of 64 bytes")}
	judged, replayed := errand.judge(_Shared{}, overran(overdue, starved))
	if !replayed || judged != _Outcome(starved) {
		t.Fatalf("a job past its limit whose shared child died of another's memory settled %#v instead of replaying", judged)
	}
	var wedgedBy = _Lost{cause: fmt.Errorf("%w: %w", errSilent, _MemoryExhausted("exceeded the memory limit of 64 bytes"))}
	var err = located("$.v", refusal(t, overran(overdue, wedgedBy)))
	expectError(t, err, WallLimit, "$.v: exceeded the wall time limit of 1ms: the evaluator stopped answering", "exceeded the memory limit of 64 bytes")
	for _, foreign := range []error{MemoryLimit, errSilent} {
		if errors.Is(err, foreign) {
			t.Fatalf("a timed-out job's error %v matches the death's %v", err, foreign)
		}
	}
}

func TestAnOutsideDeathPastALimitStillClassifiesAsUnavailable(t *testing.T) {
	overdue, stop := context.WithCancelCause(t.Context())
	stop(_WallLimit(time.Millisecond).exhausted())
	var errand = &_Errand{ctx: t.Context()}
	var outside = _Lost{cause: _OutsideDeath("evaluator died (signal: killed): killed from outside jaml")}
	judged, replayed := errand.judge(_Owned{}, overran(overdue, outside))
	if replayed {
		t.Fatal("a job alone on its child was replayed after an outside death")
	}
	var err = located("$.v", refusal(t, judged))
	expectError(t, err, Unavailable, "killed from outside jaml")
	if errors.Is(err, WallLimit) {
		t.Fatalf("an outside death past a limit was blamed on the limit: %v", err)
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
		spun <- resolveInto(WithWallLimit(t.Context(), 100*time.Millisecond), &out, small.At("spin"))
	}()
	expectEqual(t, mustResolve[int](t, WithWallLimit(t.Context(), 100*time.Millisecond), small.At("b")), 1)
	if waited := time.Since(queued); waited < 100*time.Millisecond+cancelGrace {
		t.Logf("the big request was written within %s, so the limited call barely waited", waited)
	}
	expectError(t, <-spun, WallLimit, "$.spin: ${(() => { while (true) {} })()}: exceeded the wall time limit of 100ms")
	if err := <-carried; err != nil {
		t.Fatalf("the big request failed: %v", err)
	}
	if !child.alive() {
		t.Fatalf("the evaluator died under queued work: %v", child.lost().cause)
	}
}
