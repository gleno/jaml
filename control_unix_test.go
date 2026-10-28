//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fillStoppedControls(t *testing.T, child *_Life) {
	t.Helper()
	if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var ctx = within(t, 5*time.Second)
	for {
		state, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(child.cmd.Process.Pid)).Output()
		if err != nil {
			t.Fatalf("observing the stopped evaluator: %v", err)
		}
		if strings.ContainsRune(string(state), 'T') {
			break
		}
		time.Sleep(time.Millisecond)
	}
	encoded, err := wireJSON(_Request{Op: opForget})
	if err != nil {
		t.Fatal(err)
	}
	var frame = headed(encoded)
	child.wire.control.Lock()
	defer child.wire.control.Unlock()
	raw, err := child.wire.controls.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var filled int
	var writeError error
	err = raw.Control(func(fd uintptr) {
		if writeError = syscall.SetNonblock(int(fd), true); writeError != nil {
			return
		}
		for filled <= 1<<20 {
			written, failure := syscall.Write(int(fd), frame)
			switch {
			case errors.Is(failure, syscall.EINTR):
				continue
			case errors.Is(failure, syscall.EAGAIN):
				return
			case failure != nil:
				writeError = failure
				return
			case written != len(frame):
				writeError = fmt.Errorf("short atomic control write: %d of %d bytes", written, len(frame))
				return
			}
			filled += written
		}
		writeError = fmt.Errorf("the stopped control pipe accepted more than 1 MiB")
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeError != nil {
		t.Fatal(writeError)
	}
	// A cancel has the same opcode width plus a nonzero job field, so it cannot
	// fit when this smaller, valid forget frame has reached actual EAGAIN.
	t.Logf("filled the stopped evaluator's control pipe with %d bytes", filled)
}

func TestACancelBlockedByAFullControlPipeStillEndsTheChild(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(1))
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var quick = mustExpression(t, sb, "7")
	var child = currentChild(t, sb)
	var log = recordRequests(t, sb)
	work, cancel := context.WithCancel(within(t, 20*time.Second))
	t.Cleanup(cancel)
	var spinning = make(chan error, 1)
	go func() {
		var out any
		spinning <- resolveInto(work, &out, spin)
	}()
	awaitServed(t, log, spin, 1)
	var requests = log.served(unitOf(spin))
	expectEqual(t, len(requests), 1)
	var runningBy = time.Now().Add(5 * time.Second)
	for {
		child.board.mu.Lock()
		var waiting = child.board.pending[requests[0].Job]
		var running = waiting != nil && len(waiting.used) > 0
		child.board.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(runningBy) {
			t.Fatal("the infinite job never reported running usage")
		}
		time.Sleep(time.Millisecond)
	}
	fillStoppedControls(t, child)
	var preceding = child.tell(_Request{Op: opForget, Unit: 1})
	var deadline = time.Now().Add(time.Second)
	for child.wire.control.TryLock() {
		child.wire.control.Unlock()
		select {
		case <-preceding:
			t.Fatal("the full control pipe unexpectedly accepted another control")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the preceding control never occupied the serialized writer")
		}
		time.Sleep(time.Millisecond)
	}
	var stopped = time.Now()
	cancel()
	select {
	case err := <-spinning:
		expectError(t, err, EvaluationFailed, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the cancelled caller waited for its blocked control write")
	}
	type answer struct {
		value int
		err   error
	}
	var answered = make(chan answer, 1)
	var ctx = within(t, 15*time.Second)
	go func() {
		value, err := Resolve[int](ctx, quick)
		answered <- answer{value, err}
	}()
	select {
	case <-child.ended:
	case <-time.After(backstopSilence + 3*time.Second):
		t.Fatal("the cancelled job's full control pipe prevented the silent evaluator from ending")
	}
	if waited := time.Since(stopped); waited < backstopSilence {
		t.Fatalf("the evaluator ended after %s, before its %s silence allowance", waited, backstopSilence)
	}
	if cause := child.lost().cause; !strings.Contains(cause.Error(), "the evaluator stopped answering") {
		t.Fatalf("the silent evaluator's death was judged %v", cause)
	}
	select {
	case got := <-answered:
		if got.err != nil {
			t.Fatal(got.err)
		}
		expectEqual(t, got.value, 7)
	case <-time.After(3 * time.Second):
		t.Fatal("the cancelled job never released its slot for a replacement evaluator")
	}
	if replacement := childPID(t, sb); replacement == child.cmd.Process.Pid {
		t.Fatal("the quick job did not use a replacement evaluator")
	}
}

func TestAFailedControlWriteRetiresTheChild(t *testing.T) {
	var sb = mustIsolated(t)
	var quick = mustExpression(t, sb, "7")
	var child = currentChild(t, sb)
	if err := child.wire.controls.Close(); err != nil {
		t.Fatal(err)
	}
	child.tell(_Request{Op: opForget})
	select {
	case <-child.ended:
	case <-time.After(time.Second):
		t.Fatal("a failed control write left an evaluator alive without a usable control pipe")
	}
	expectEqual(t, mustResolve[int](t, within(t, 5*time.Second), quick), 7)
	if replacement := childPID(t, sb); replacement == child.cmd.Process.Pid {
		t.Fatal("the quick job did not use a replacement evaluator")
	}
}
