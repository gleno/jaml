//go:build unix && !ios

package jaml

import (
	"syscall"
	"testing"
	"time"
)

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
	var err = resolveInto(WithWallLimit(t.Context(), 50*time.Millisecond), &out, runaway)
	expectError(t, err, WallLimit, "$: /^(?=a)(a+)+$/.test('a'.repeat(30) + 'b'): exceeded the wall time limit of 50ms and was still inside a built-in 250ms after it was told to stop")
	expectStuckAfterCancel(t, child)
	if err := <-survived; err != nil {
		t.Fatalf("a job beside the stuck one failed: %v", err)
	}
}

func TestAStuckRunNamesThePieceAndQuotesItsCode(t *testing.T) {
	_SandboxKinds{isolatedKind, unprotectedKind}.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "y:\n  - ${ /^(?=a)(a+)+$/.test('a'.repeat(30) + 'b') }\n")
		var child = currentChild(t, sb)
		var out any
		var err = resolveInto(WithWallLimit(t.Context(), 50*time.Millisecond), &out, document.At("y"))
		expectError(t, err, WallLimit, "$.y[0]: ${/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')}: exceeded the wall time limit of 50ms and was still inside a built-in")
		expectFailure(t, err, "$.y[0]", "exceeded the wall time limit of 50ms and was still inside a built-in 250ms after it was told to stop")
		expectStuckAfterCancel(t, child)
	})
}

func TestAWedgedChildIsKilledAtTheWallLimitOnceItsScriptsCompiled(t *testing.T) {
	var sb = mustIsolated(t, WithBuildTimeout(20*time.Second))
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var child = currentChild(t, sb)
	var answered = make(chan error, 1)
	var started = time.Now()
	go func() {
		var out any
		answered <- resolveInto(WithWallLimit(WithScript(t.Context(), "const compiled = 1"), 300*time.Millisecond), &out, spin)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the evaluator: %v", err)
	}
	var err = <-answered
	var waited = time.Since(started)
	expectError(t, err, WallLimit, "exceeded the wall time limit of 300ms", "the evaluator stopped answering")
	if waited > 300*time.Millisecond+2*cancelGrace+backstopSilence+3*time.Second {
		t.Fatalf("a wedged child whose scripts had compiled was killed only after %s, as if the build timeout still counted", waited)
	}
	if waited < 300*time.Millisecond+backstopSilence {
		t.Fatalf("a wedged child was killed after %s, before its wall limit and the backstop's silence", waited)
	}
}

func TestAJobWhoseChildIsKilledStillReportsItsLastUsage(t *testing.T) {
	var sb = mustIsolated(t)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var reported []Usage
	var answered = make(chan error, 1)
	go func() {
		var out any
		answered <- resolveInto(WithMeter(WithWallLimit(t.Context(), 20*time.Second), func(used Usage) { reported = append(reported, used) }), &out, spin)
	}()
	var first = currentChild(t, sb)
	time.Sleep(2*usageEvery + 100*time.Millisecond)
	var known = ourChildren(t)
	if err := first.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing the evaluator: %v", err)
	}
	<-first.ended
	if replay := awaitNewChild(t, known, answered); replay != 0 {
		time.Sleep(2*usageEvery + 100*time.Millisecond)
		_ = syscall.Kill(replay, syscall.SIGKILL)
	}
	var err = <-answered
	expectError(t, err, Unavailable, "killed from outside jaml")
	if len(reported) != 1 || reported[0].Wall < usageEvery {
		t.Fatalf("a job whose child was killed twice reported %+v, want one usage at least %s old", reported, usageEvery)
	}
}
