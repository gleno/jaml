//go:build unix && !ios

package jaml

import (
	"syscall"
	"testing"
	"time"
)

func TestADeadEvaluatorIsReplaced(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "value: ${ 7 }\nok: ${ 4 }\n")
	var first = currentChild(t, sb)
	var log = recordRequests(t, sb)
	if err := first.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		value int
		err   error
	}
	var answered = make(chan answer, 1)
	var ctx = within(t, 10*time.Second)
	go func() {
		value, err := Resolve[int](ctx, document.At("value"))
		answered <- answer{value, err}
	}()
	awaitServed(t, log, document, 1)
	var attempts = log.served(unitOf(document))
	expectEqual(t, len(attempts), 1)
	if !first.board.awaits(attempts[0].Job) {
		t.Fatal("the stopped evaluator had no pending request when it was killed")
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	expectDeath(t, first)
	select {
	case got := <-answered:
		if got.err != nil {
			t.Fatal(got.err)
		}
		expectEqual(t, got.value, 7)
	case <-time.After(10 * time.Second):
		t.Fatal("the dead evaluator's pending request was never replayed")
	}
	expectEqual(t, len(log.served(unitOf(document))), 2)
	if replayed := log.lastServed().life; replayed == first.cmd.Process.Pid {
		t.Fatal("the request was not replayed in a replacement evaluator")
	}
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "ok"), 4)
	if replaced := childPID(t, sb); replaced == first.cmd.Process.Pid {
		t.Fatalf("expected a fresh evaluator, still %d", replaced)
	}
}
