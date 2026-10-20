//go:build unix && !ios

package jaml

import (
	"errors"
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

func killFromOutside(t *testing.T, child *_Life) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the evaluator: %v", err)
	}
}

func TestAJobWhoseReplayIsKilledFromOutsideIsUnavailable(t *testing.T) {
	var sb = mustIsolated(t)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var started = recordStarts(t, sb)
	var first = currentChild(t, sb)
	var spun = make(chan error, 1)
	go func() {
		var out any
		spun <- resolveInto(within(t, 30*time.Second), &out, spin)
	}()
	killFromOutside(t, first)
	select {
	case replay := <-started:
		killFromOutside(t, replay)
	case <-time.After(10 * time.Second):
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
	var slow = mustExpression(t, sb, "(() => { let n = 0; for (let i = 0; i < 3e7; i++) { n += i % 3 } return n > 0 })()")
	var first = currentChild(t, sb)
	var done = make(chan error, 1)
	go func() {
		var out bool
		done <- resolveInto(within(t, 60*time.Second), &out, slow)
	}()
	killFromOutside(t, first)
	if err := <-done; err != nil {
		t.Fatalf("a job on a child killed from outside was not replayed: %v", err)
	}
}
