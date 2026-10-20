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

func TestARunStuckInABuiltInPastItsTimeLimitEndsTheChildAndFailsWithTimeLimit(t *testing.T) {
	var sb = mustIsolated(t)
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var neighbour = mustExpression(t, sb, "(() => { let n = 0; for (let i = 0; i < 3e7; i++) { n += i % 3 } return n > 0 })()")
	var child = currentChild(t, sb)
	var survived = make(chan error, 1)
	go func() {
		var out bool
		survived <- resolveInto(t.Context(), &out, neighbour)
	}()
	var out any
	var err = resolveInto(WithTimeLimit(t.Context(), 50*time.Millisecond), &out, runaway)
	expectError(t, err, TimeLimit, "$: exceeded the time limit of 50ms and was still inside a built-in 250ms after it was told to stop")
	expectStuckAfterCancel(t, child)
	if err := <-survived; err != nil {
		t.Fatalf("a job beside the stuck one failed: %v", err)
	}
}
