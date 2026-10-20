//go:build unix && !ios

package jaml

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func recordStarts(t *testing.T, sb *Sandbox) <-chan *_Life {
	t.Helper()
	var d = dispatcherOf(t, sb)
	var started = make(chan *_Life, 8)
	d.mu.Lock()
	var start = d.start
	d.start = func() (*_Life, error) {
		born, err := start()
		if err == nil {
			started <- born
		}
		return born, err
	}
	d.mu.Unlock()
	return started
}

func holdChild(t *testing.T, child *_Life) {
	t.Helper()
	if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the evaluator: %v", err)
	}
	t.Cleanup(func() { _ = child.cmd.Process.Signal(syscall.SIGCONT) })
}

func killMidJob(t *testing.T, child *_Life) {
	t.Helper()
	var deadline = time.Now().Add(30 * time.Second)
	for !serving(child) {
		if time.Now().After(deadline) {
			t.Fatal("the evaluator was never handed the job")
		}
		time.Sleep(time.Millisecond)
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the evaluator: %v", err)
	}
}

func serving(child *_Life) bool {
	child.board.mu.Lock()
	defer child.board.mu.Unlock()
	for _, expectation := range child.board.pending {
		if expectation.op == opServe {
			return true
		}
	}
	return false
}

func TestAJobWhoseReplayIsKilledFromOutsideIsUnavailable(t *testing.T) {
	var sb = mustIsolated(t)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var started = recordStarts(t, sb)
	var first = currentChild(t, sb)
	var spun = make(chan error, 1)
	go func() {
		var out any
		spun <- resolveInto(within(t, 60*time.Second), &out, spin)
	}()
	killMidJob(t, first)
	select {
	case replay := <-started:
		killMidJob(t, replay)
	case <-time.After(30 * time.Second):
		t.Fatal("the killed job was never replayed")
	}
	var err = <-spun
	expectError(t, err, Unavailable, "killed from outside jaml")
	if errors.Is(err, EvaluationFailed) {
		t.Fatalf("a death jaml did not cause blamed the code: %v", err)
	}
}

func TestAJobWhoseSharedChildIsKilledFromOutsideIsReplayed(t *testing.T) {
	var sb = mustIsolated(t)
	var answer = mustExpression(t, sb, "(() => 6 * 7)()")
	var started = recordStarts(t, sb)
	var first = currentChild(t, sb)
	holdChild(t, first)
	var done = make(chan error, 1)
	var out int
	go func() {
		done <- resolveInto(within(t, 60*time.Second), &out, answer)
	}()
	killMidJob(t, first)
	if err := <-done; err != nil {
		t.Fatalf("a job on a child killed from outside was not replayed: %v", err)
	}
	expectEqual(t, out, 42)
	var replay = <-started
	select {
	case <-replay.ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the evaluator that replayed the job alone outlived it")
	}
	if !errors.Is(replay.killReason(), errRetired) {
		t.Fatalf("the evaluator that replayed the job ended by %v, not retired after it", replay.killReason())
	}
}
