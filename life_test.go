//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func slowBuild(seconds float64) string {
	return listOfPlainServers(int(seconds * 250000 / (2*raceSlowdown - 1)))
}

func TestAChildThatPanicsBeforeGreetingSaysWhy(t *testing.T) {
	_, err := MakeSecureSandbox(within(t, 10*time.Second), WithMemoryLimit(panicAtStartLimit))
	expectUnavailable(t, err, "$: starting the evaluator", "evaluator died (exit status 2): panic: an impostor's init")
	if errors.Is(err, Unsupported) {
		t.Fatalf("a child that died before greeting is not a foreign executable: %v", err)
	}
}

func TestACancelledBuildLeavesTheChildAlone(t *testing.T) {
	var sb = mustIsolated(t)
	var before = childPID(t, sb)
	var text = slowBuild(1)
	var started = time.Now()
	_, err := sb.MakeExpressionFromJaml(within(t, 20*time.Millisecond), text)
	expectError(t, err, EvaluationFailed, "building: cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("a cancelled build held its caller for %s past a 20ms deadline", waited)
	}
	time.Sleep(time.Second)
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	expectEqual(t, childPID(t, sb), before)
}

func TestACancelledRebuildKeepsItsSlotAndLeavesTheChildAlone(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(1), WithMemoryLimit(512<<20))
	var big = mustJaml(t, sb, slowBuild(0.4)+"v: ${ 1 }\n")
	var small = mustJaml(t, sb, "v: ${ 2 }\n")
	killChild(t, sb)
	expectEqual(t, isolatedEvalAs[int](t, small, nil, "v"), 2)
	var respawned = childPID(t, sb)
	var started = time.Now()
	var out int
	expectError(t, resolveInto(within(t, 10*time.Millisecond), &out, big.At("v")), EvaluationFailed, "$.v: cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("a cancelled rebuild held its caller for %s past a 10ms deadline", waited)
	}
	expectEqual(t, isolatedEvalAs[int](t, small, nil, "v"), 2)
	if waited := time.Since(started); waited < 250*time.Millisecond {
		t.Fatalf("a queued Eval ran after %s, before the rebuild it waited on could answer", waited)
	}
	expectEqual(t, isolatedEvalAs[int](t, big, nil, "v"), 1)
	expectEqual(t, childPID(t, sb), respawned)
}

func TestARunawayBuiltInReturnsAtItsDeadline(t *testing.T) {
	var sb = mustIsolated(t)
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var before = childPID(t, sb)
	var started = time.Now()
	var out any
	expectError(t, resolveInto(within(t, 20*time.Millisecond), &out, runaway), EvaluationFailed, "$: cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 150*time.Millisecond {
		t.Fatalf("a runaway built-in held its caller for %s past a 20ms deadline", waited)
	}
	var deadline = time.Now().Add(5 * time.Second)
	for childPID(t, sb) == before {
		if time.Now().After(deadline) {
			t.Fatal("the runaway built-in was never killed")
		}
		expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 1 }\n"), nil, "v"), 1)
		time.Sleep(20 * time.Millisecond)
	}
}

func crowdedTemplate(index int) string {
	var written strings.Builder
	var padding = strings.Repeat("p", 400)
	for key := range 9500 {
		fmt.Fprintf(&written, "k%d: \"%s%d-%d\"\n", key, padding, index, key)
	}
	written.WriteString("e: ${ x + 1 }\n")
	return written.String()
}

func TestJobsThatOverflowTogetherInACrowdSucceedOnReplay(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(24<<20), WithMaxConcurrency(8), WithBuildTimeout(30*time.Second))
	var documents = make([]*Expression, 8)
	for index := range documents {
		documents[index] = mustJaml(t, sb, crowdedTemplate(index))
	}
	for round := range 2 {
		var group sync.WaitGroup
		for index, document := range documents {
			group.Go(func() {
				var out int
				if err := resolveInto(valued(within(t, 120*time.Second), map[string]any{"x": index}), &out, document.At("e")); err != nil || out != index+1 {
					t.Errorf("round %d, document %d: got %d, %v", round, index, out, err)
				}
			})
		}
		group.Wait()
	}
}

func TestACancelBehindABigRequestStillLands(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(512<<20), WithMaxConcurrency(4))
	var spin = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\n")
	var before = childPID(t, sb)
	var huge = "v: '" + strings.Repeat("\x01", (16<<20)/raceSlowdown) + "'\n"
	var spinning = make(chan error, 1)
	go func() {
		var out any
		spinning <- resolveInto(within(t, 150*time.Millisecond), &out, spin.At("spin"))
	}()
	time.Sleep(50 * time.Millisecond)
	_, err := sb.MakeExpressionFromJaml(within(t, 60*time.Second), huge)
	expectError(t, err, InvalidExpression)
	expectError(t, <-spinning, EvaluationFailed, "$.spin: cancelled")
	expectEqual(t, childPID(t, sb), before)
}

func TestForgottenRebuildsLeaveNothingResident(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithBuildTimeout(30*time.Second), WithMaxConcurrency(1))
	var documents = make([]*Expression, 12/min(raceSlowdown, 3))
	var ends = make([]context.CancelFunc, len(documents))
	for index := range documents {
		ctx, end := context.WithCancel(t.Context())
		made, err := sb.MakeExpressionFromJaml(ctx, listOfPlainServers(30000)+fmt.Sprintf("v: %d\n", index))
		if err != nil {
			t.Fatal(err)
		}
		documents[index], ends[index] = made, end
		killChild(t, sb)
	}
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	var before = childPID(t, sb)
	for index, document := range documents {
		var resolved = make(chan error, 1)
		go func() {
			var out int
			resolved <- resolveInto(t.Context(), &out, document.At("v"))
		}()
		var resume = haltMidRebuild(t, currentChild(t, sb), document)
		ends[index]()
		expectError(t, <-resolved, Closed)
		resume()
	}
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	expectEqual(t, childPID(t, sb), before)
}

func haltMidRebuild(t *testing.T, child *_Life, document *Expression) func() {
	t.Helper()
	var unit = document.held.keep.unit.(*_RemoteUnit)
	var pid = child.cmd.Process.Pid
	var deadline = time.Now().Add(30 * time.Second)
	for {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			t.Fatalf("the evaluator holding the forgotten rebuilds is gone: %v", err)
		}
		if rebuilding(child, unit) {
			return func() { _ = syscall.Kill(pid, syscall.SIGCONT) }
		}
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the forgotten document was never rebuilt")
		}
		time.Sleep(time.Millisecond)
	}
}

func rebuilding(child *_Life, unit *_RemoteUnit) bool {
	child.wire.mu.Lock()
	defer child.wire.mu.Unlock()
	var residency = child.wire.residents[unit]
	if residency == nil || residency.build == 0 {
		return false
	}
	select {
	case <-residency.settled:
		return false
	default:
		return true
	}
}

func TestACallCancelledWhileQueuedNamesItsPath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 }\nlist: [\"${ 1 }\"]\n")
		var spinning = make(chan error, 1)
		go func() {
			var out any
			spinning <- resolveInto(within(t, time.Second), &out, document.At("spin"))
		}()
		time.Sleep(100 * time.Millisecond)
		expectError(t, evalBounded(t, document, within(t, 100*time.Millisecond), "ok"), EvaluationFailed, "$.ok: cancelled")
		var out any
		expectError(t, resolveInto(within(t, 100*time.Millisecond), &out, document.At("list", "0")), EvaluationFailed, "$.list[0]: cancelled")
		<-spinning
	}, WithMaxConcurrency(1))
}

func TestAFailingRespawnFailsEveryQueuedJobOnce(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(8))
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	respawnAs(t, sb, _ChildSpec{limit: silentLimit, scriptBudget: trustedScriptBudget, startDeadline: 300 * time.Millisecond, buildTimeout: defaultBuildTimeout})
	killChild(t, sb)
	var started = time.Now()
	var failures = make(chan error, 6)
	for range 6 {
		go func() {
			var out int
			failures <- resolveInto(within(t, 30*time.Second), &out, document.At("v"))
		}()
	}
	var first error
	for range 6 {
		var err = <-failures
		expectUnavailable(t, err, "$.v: starting the evaluator", "took longer than 300ms to start")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in %v", err)
		}
		if first == nil {
			first = err
		}
		expectEqual(t, err.Error(), first.Error())
	}
	if waited := time.Since(started); waited > 900*time.Millisecond {
		t.Fatalf("six queued jobs behind one failing start took %s, not one 300ms start", waited)
	}
}

func TestAChildKilledMidRequestIsNamedByTheReaper(t *testing.T) {
	var sb = mustIsolated(t)
	var process = currentChild(t, sb)
	var pid = process.cmd.Process.Pid
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var sent = make(chan error, 1)
	go func() {
		_, err := process.send(t.Context(), planFor(t, _Blueprint{Written: strings.Repeat("x", 4<<20)}))
		sent <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if err := <-sent; !errors.Is(err, errLost) {
		t.Fatalf("a request to a killed child: %v", err)
	}
	expectDeath(t, process)
	if !strings.Contains(process.verdict.Error(), "killed from outside jaml") {
		t.Fatalf("the verdict: %v", process.verdict)
	}
}

func TestAHostThatReapsItsOwnChildrenOutlivesThem(t *testing.T) {
	if os.Getenv("JAML_REAPING_HOST") == "1" {
		signal.Ignore(syscall.SIGCHLD)
		var foreign = currentChild(t, mustIsolated(t))
		_ = syscall.Kill(foreign.cmd.Process.Pid, syscall.SIGKILL)
		expectDeath(t, foreign)
		var ours = currentChild(t, mustIsolated(t))
		ours.kill(errors.New("ours to end"))
		expectDeath(t, ours)
		os.Stdout.WriteString("FOREIGN " + foreign.verdict.Error() + "\nOURS " + ours.verdict.Error() + "\n")
		return
	}
	self, _ := os.Executable()
	var cmd = exec.Command(self, "-test.run", "^TestAHostThatReapsItsOwnChildrenOutlivesThem$")
	cmd.Env = append(os.Environ(), "JAML_REAPING_HOST=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the reaping host died: %v\n%s", err, output)
	}
	for _, want := range []string{"FOREIGN evaluator ended but its exit could not be collected", "OURS ours to end"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("want %q in\n%s", want, output)
		}
	}
}

const (
	stragglerLimit       = 64<<20 + 11
	silentStragglerLimit = 16<<20 + 12
)

var _ = misbehaveAtTheEnd()

func misbehaveAtTheEnd() bool {
	if os.Getenv(evaluatorMarker) != "1" {
		return false
	}
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(stragglerLimit):
		leaveAStraggler()
	case strconv.Itoa(silentStragglerLimit):
		leaveAStraggler()
		time.Sleep(time.Hour)
	default:
		return false
	}
	return true
}

func leaveAStraggler() {
	var straggler = exec.Command("/bin/sleep", "10")
	straggler.Stderr = os.Stderr
	straggler.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if straggler.Start() != nil {
		return
	}
	register, err := os.OpenFile(stragglerRegister(os.Getenv(evaluatorParent)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer register.Close()
	fmt.Fprintln(register, straggler.Process.Pid)
}

func stragglerRegister(host string) string {
	self, _ := os.Executable()
	return filepath.Join(filepath.Dir(self), "stragglers-"+host)
}

func reapStragglers(t *testing.T) {
	var register = stragglerRegister(strconv.Itoa(os.Getpid()))
	t.Cleanup(func() {
		recorded, _ := os.ReadFile(register)
		for _, line := range strings.Fields(string(recorded)) {
			if pid, err := strconv.Atoi(line); err == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		_ = os.Remove(register)
	})
}

func TestAReplyDeliveredBeforeTheEndWinsOverIt(t *testing.T) {
	for range 50 {
		var outcome = serveIntoAnEndingLife(t)
		if _, answered := outcome.(_Answered); !answered {
			t.Fatalf("a reply delivered before the life ended came back as %#v", outcome)
		}
	}
}

func serveIntoAnEndingLife(t *testing.T) _Outcome {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll(reader, writer)
	fillPipe(t, writer)
	var life = &_Life{reaped: make(chan struct{}), ended: make(chan struct{}), wire: newWire(writer, writer), board: &_Switchboard{pending: map[uint64]*_Expectation{}}}
	var served = make(chan _Outcome, 1)
	go func() { served <- life.serve(t.Context(), askFor(t, 1, _Ask{})) }()
	for !life.board.awaits(1) {
		time.Sleep(time.Millisecond)
	}
	if err := life.board.deliver(_Reply{Job: 1, Data: []byte("1"), At: "$"}); err != nil {
		t.Fatal(err)
	}
	life.bury(errors.New("the child died after answering"))
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	return <-served
}

func fillPipe(t *testing.T, writer *os.File) {
	t.Helper()
	var chunk = make([]byte, 4096)
	_ = writer.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	for {
		if _, err := writer.Write(chunk); err != nil {
			break
		}
	}
	_ = writer.SetWriteDeadline(time.Time{})
}

func (b *_Switchboard) awaits(job uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending[job] != nil
}

func TestAChildLeavingAStragglerIsStillBuried(t *testing.T) {
	reapStragglers(t)
	var sb = mustIsolated(t, WithMemoryLimit(stragglerLimit))
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 1)
	var process = currentChild(t, sb)
	var started = time.Now()
	process.kill(errors.New("ours to end"))
	expectDeath(t, process)
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("a child leaving a straggler took %s to bury", waited)
	}
	expectEqual(t, process.verdict.Error(), "ours to end")
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 1)
}

func TestAStartLeavingAStragglerKeepsItsDeadline(t *testing.T) {
	reapStragglers(t)
	var started = time.Now()
	_, err := MakeSecureSandbox(within(t, 300*time.Millisecond), WithMemoryLimit(silentStragglerLimit))
	expectUnavailable(t, err, "starting the evaluator")
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("a start leaving a straggler held its caller for %s past a 300ms deadline", waited)
	}
}
