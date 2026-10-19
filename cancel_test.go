//go:build unix && !ios

package jaml

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	heldReplyLimit   = 256<<20 + 11
	markedReplyLimit = 256<<20 + 13
	heldReplyFor     = 3 * time.Second
	heldReplyBytes   = 1 << 20
)

var _ = holdBigReplies()

func holdBigReplies() bool {
	if os.Getenv(evaluatorMarker) != "1" {
		return false
	}
	var marker string
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(heldReplyLimit):
	case strconv.Itoa(markedReplyLimit):
		marker = heldReplyMarker(os.Getpid())
	default:
		return false
	}
	os.Exit(serveEvaluations(_HeldReplies{out: os.NewFile(responsesFD, "held-replies"), marker: marker}))
	return true
}

type _HeldReplies struct {
	out    io.Writer
	marker string
}

func (h _HeldReplies) Write(written []byte) (int, error) {
	if len(written) > heldReplyBytes {
		if h.marker != "" {
			if err := os.WriteFile(h.marker, nil, 0o600); err != nil {
				return 0, err
			}
		}
		time.Sleep(heldReplyFor)
	}
	return h.out.Write(written)
}

func heldReplyMarker(child int) string {
	return filepath.Join("/tmp", "jaml-held-reply-"+strconv.Itoa(child))
}

func awaitHeldReply(t *testing.T, child int) {
	t.Helper()
	var marker = heldReplyMarker(child)
	t.Cleanup(func() { os.Remove(marker) })
	var deadline = time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the big reply never reached its held write")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bigAnswer() string {
	return `"x".repeat(` + strconv.Itoa(2*heldReplyBytes) + `)`
}

func resolvingBig(t *testing.T, sb *Sandbox) <-chan error {
	t.Helper()
	var big = mustExpression(t, sb, bigAnswer())
	var answered = make(chan error, 1)
	go func() {
		var out string
		var err = resolveInto(within(t, 30*time.Second), &out, big)
		if err == nil && len(out) != 2*heldReplyBytes {
			t.Errorf("the big answer came back %d bytes long", len(out))
		}
		answered <- err
	}()
	time.Sleep(raceSlowdown * 100 * time.Millisecond)
	return answered
}

func expectStuckAfterCancel(t *testing.T, child *_Life) {
	t.Helper()
	select {
	case <-child.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("a job stuck after its cancel never ended its evaluator")
	}
	if cause := child.lost().cause; !strings.Contains(cause.Error(), "stuck after cancel") {
		t.Fatalf("the evaluator's death was judged %v, want a job stuck after cancel", cause)
	}
}

func expectSurvived(t *testing.T, child *_Life) {
	t.Helper()
	if !child.alive() {
		t.Fatalf("the evaluator died: %v", child.lost().cause)
	}
}

func expectAllAnswered(t *testing.T, child *_Life) {
	t.Helper()
	var deadline = time.Now().Add(60 * time.Second)
	for {
		child.board.mu.Lock()
		var pending = len(child.board.pending)
		child.board.mu.Unlock()
		if pending == 0 || !child.alive() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d jobs were never answered", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestALateCancelOnABigReplyLeavesTheChildAlone(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(markedReplyLimit))
	var big = mustExpression(t, sb, bigAnswer())
	var child = currentChild(t, sb)
	ctx, cancel := context.WithCancel(within(t, 60*time.Second))
	var answered = make(chan error, 1)
	go func() {
		var out string
		answered <- resolveInto(ctx, &out, big)
	}()
	awaitHeldReply(t, child.cmd.Process.Pid)
	cancel()
	expectError(t, <-answered, EvaluationFailed, "cancelled")
	expectAllAnswered(t, child)
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 3 }\n"), nil, "v"), 3)
	expectSurvived(t, child)
}

func TestASiblingQueuedBehindAHeldFrameIsNotReplayed(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(heldReplyLimit), WithMaxConcurrency(4))
	var small = mustExpression(t, sb, "1")
	var before = childPID(t, sb)
	var answered = resolvingBig(t, sb)
	var out int
	expectError(t, resolveInto(within(t, 100*time.Millisecond), &out, small), EvaluationFailed, "cancelled")
	if err := <-answered; err != nil {
		t.Fatalf("the big answer failed: %v", err)
	}
	expectEqual(t, childPID(t, sb), before)
}

func TestABuildOverdueBehindAHeldFrameLeavesTheChildAlone(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(heldReplyLimit), WithMaxConcurrency(4), WithBuildTimeout(300*time.Millisecond))
	var before = childPID(t, sb)
	var answered = resolvingBig(t, sb)
	_, err := sb.MakeExpressionFromJaml(t.Context(), "v: 1\n")
	expectError(t, err, EvaluationFailed, "the build took longer than its timeout of 300ms")
	if err := <-answered; err != nil {
		t.Fatalf("the big answer failed: %v", err)
	}
	expectEqual(t, childPID(t, sb), before)
}

func TestAServeStuckAfterCancelEndsTheChildAsStuck(t *testing.T) {
	var sb = mustIsolated(t)
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var child = currentChild(t, sb)
	var out any
	expectError(t, resolveInto(within(t, 20*time.Millisecond), &out, runaway), EvaluationFailed, "$: cancelled", "deadline exceeded")
	expectStuckAfterCancel(t, child)
}

func TestABuildStuckPastItsTimeoutEndsTheChildAsStuck(t *testing.T) {
	var sb = mustIsolated(t, WithBuildTimeout(100*time.Millisecond))
	var child = currentChild(t, sb)
	_, err := sb.MakeExpressionFromJaml(t.Context(), hostileText())
	expectError(t, err, EvaluationFailed, "cancelled: the build took longer than its timeout of 100ms")
	expectStuckAfterCancel(t, child)
}

func TestAWedgedChildIsKilledByTheBackstop(t *testing.T) {
	var sb = mustIsolated(t)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var child = currentChild(t, sb)
	var spinning = make(chan error, 1)
	go func() {
		var out any
		spinning <- resolveInto(within(t, 200*time.Millisecond), &out, spin)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the evaluator: %v", err)
	}
	var stopped = time.Now()
	expectError(t, <-spinning, EvaluationFailed, "cancelled")
	select {
	case <-child.ended:
	case <-time.After(backstopSilence + 10*time.Second):
		t.Fatal("a wedged evaluator was never killed")
	}
	if waited := time.Since(stopped); waited < backstopSilence {
		t.Fatalf("a wedged evaluator was killed %s after it stopped, before the backstop's %s of silence", waited, backstopSilence)
	}
	if cause := child.lost().cause; !strings.Contains(cause.Error(), "the evaluator stopped answering") {
		t.Fatalf("the wedged evaluator's death was judged %v", cause)
	}
}

func TestAChildNotReadingRequestsIsReplaced(t *testing.T) {
	var sb = mustIsolated(t)
	var length = mustExpression(t, sb, "x.length")
	var child = currentChild(t, sb)
	if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the evaluator: %v", err)
	}
	var stopped = time.Now()
	var out int
	expectError(t, resolveInto(valued(within(t, 200*time.Millisecond), map[string]any{"x": strings.Repeat("y", 4<<20)}), &out, length), EvaluationFailed, "cancelled")
	if waited := time.Since(stopped); waited > time.Second {
		t.Fatalf("a call blocked on a stopped evaluator returned after %s, past its 200ms context", waited)
	}
	select {
	case <-child.ended:
	case <-time.After(backstopSilence + 10*time.Second):
		t.Fatal("an evaluator that stopped reading requests was never killed")
	}
	if cause := child.lost().cause; !strings.Contains(cause.Error(), "not reading requests") {
		t.Fatalf("the stopped evaluator's death was judged %v", cause)
	}
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 5 }\n"), nil, "v"), 5)
}
