//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func unitOn(t *testing.T, text string) *_RemoteUnit {
	t.Helper()
	made, _ := endingUnitOn(t, text)
	return made
}

func endingUnitOn(t *testing.T, text string) (*_RemoteUnit, context.CancelFunc) {
	t.Helper()
	ctx, end := context.WithCancel(t.Context())
	t.Cleanup(end)
	return &_RemoteUnit{plan: _Blueprint{Written: text, Null: Loose, Undefined: Strict}, lifetime: ctx}, end
}

func ignoreEarly(_Outcome) {}

func residentBuild(t *testing.T, child *_Life, u *_RemoteUnit) uint64 {
	t.Helper()
	var deadline = time.Now().Add(10 * time.Second)
	for {
		if r := residencyOf(child, u); r != nil {
			child.wire.mu.Lock()
			var build = r.build
			child.wire.mu.Unlock()
			if build != 0 {
				return build
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the unit never took its place on the wire")
		}
		time.Sleep(time.Millisecond)
	}
}

func expectMissing(t *testing.T, child *_Life, build uint64) {
	t.Helper()
	var served = child.serve(within(t, 10*time.Second), build, _Work{})
	if failed, isRefused := served.(_Refused); !isRefused || !errors.Is(failed.err, errMissing) {
		t.Fatalf("the evaluator still serves build %d: %v", build, served)
	}
}

func expectServes(t *testing.T, child *_Life, build uint64, want string) {
	t.Helper()
	var served = child.serve(within(t, 10*time.Second), build, _Work{Path: []string{"v"}})
	data, isAnswered := served.(_Answered)
	if !isAnswered || string(data.data) != want {
		t.Fatalf("build %d served %v, want %s", build, served, want)
	}
}

func deliver(t *testing.T, child *_Life, outgoing _Request) {
	t.Helper()
	select {
	case <-child.tell(outgoing):
	case <-time.After(10 * time.Second):
		t.Fatal("a control message was never delivered")
	}
}

func TestAForgetDuringItsBuildLeavesNothingServable(t *testing.T) {
	var child = currentChild(t, mustIsolated(t, WithMemoryLimit(512<<20)))
	u, end := endingUnitOn(t, slowBuild(0.5)+"v: 1\n")
	var settled = make(chan *_Residency, 1)
	go func() { settled <- child.reside(u, ignoreEarly) }()
	var build = residentBuild(t, child, u)
	time.Sleep(20 * time.Millisecond)
	end()
	var r = <-settled
	if _, built := r.result.(_Answered); built {
		time.Sleep(50 * time.Millisecond)
	}
	expectMissing(t, child, build)
	if residencyOf(child, u) != nil {
		t.Fatal("an ended unit is still in the ledger")
	}
}

func TestAForgetThatOvertakesItsBuildIsHonoured(t *testing.T) {
	var child = currentChild(t, mustIsolated(t))
	child.wire.writer <- struct{}{}
	var next = child.wire.sent + 1
	<-child.wire.writer
	deliver(t, child, _Request{Op: opForget, Unit: next})
	var u = unitOn(t, "v: 1\n")
	var r = child.reside(u, ignoreEarly)
	expectEqual(t, r.build, next)
	if failed, isRefused := r.result.(_Refused); !isRefused || !strings.Contains(failed.err.Error(), "cancelled") {
		t.Fatalf("a build forgotten before it arrived was answered %v", r.result)
	}
	expectMissing(t, child, next)
	expectServes(t, child, child.reside(unitOn(t, "v: 2\n"), ignoreEarly).build, "2")
}

func TestARebuildSurvivesAStaleForget(t *testing.T) {
	var child = currentChild(t, mustIsolated(t))
	var u = unitOn(t, "v: 1\n")
	var first = child.reside(u, ignoreEarly)
	child.drop(first)
	child.wire.mu.Lock()
	var rebuilt = make(chan *_Residency, 1)
	go func() { rebuilt <- child.reside(u, ignoreEarly) }()
	time.Sleep(20 * time.Millisecond)
	deliver(t, child, _Request{Op: opForget, Unit: first.build})
	child.wire.mu.Unlock()
	var second = <-rebuilt
	deliver(t, child, _Request{Op: opForget, Unit: first.build})
	if second.build == first.build {
		t.Fatalf("a rebuild reused build id %d", first.build)
	}
	expectServes(t, child, second.build, "1")
	expectMissing(t, child, first.build)
}

func TestAStaleDropLeavesANewerResidency(t *testing.T) {
	var child = currentChild(t, mustIsolated(t))
	var u = unitOn(t, "v: 1\n")
	var older = child.reside(u, ignoreEarly)
	child.drop(older)
	var newer = child.reside(u, ignoreEarly)
	child.drop(older)
	if residencyOf(child, u) != newer {
		t.Fatal("dropping an older residency removed the newer one")
	}
}

func TestAUnitTheChildLostIsBuiltAgain(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "v: ${ 4 }\n")
	var child = currentChild(t, sb)
	var build = residentBuild(t, child, document.held.keep.unit.(*_RemoteUnit))
	deliver(t, child, _Request{Op: opForget, Unit: build})
	expectMissing(t, child, build)
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 4)
	expectEqual(t, currentChild(t, sb), child)
}

func exited(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	var cmd = exec.Command("sh", "-c", script)
	_ = cmd.Run()
	return cmd
}

func TestADeathIsClassifiedFromTheEvidence(t *testing.T) {
	var ours = errors.New("cancelled: context deadline exceeded")
	var marked = &_HeadBuffer{limit: stderrHeadBytes}
	_, _ = marked.Write([]byte(pressureMarker + "\n"))
	var panicked = &_HeadBuffer{limit: stderrHeadBytes}
	_, _ = panicked.Write([]byte(pressureMarker + "\npanic: boom\n\ngoroutine 1 [running]:\n"))
	var silent = &_HeadBuffer{limit: stderrHeadBytes}
	var cases = map[string]struct {
		ending  _Ending
		remains _Remains
		memory  bool
		want    string
	}{
		"an exit 86 beats our kill":           {_Collected{state: exited(t, "exit 86").ProcessState}, _Remains{stderr: silent, killCause: ours, limit: 64 << 20}, true, "exceeded the memory limit of 67108864 bytes"},
		"a panic beats our kill":              {_Collected{state: exited(t, "exit 2").ProcessState}, _Remains{stderr: panicked, killCause: ours, limit: 64 << 20}, false, "evaluator died (exit status 2): panic: boom"},
		"our kill names its reason":           {_Collected{state: exited(t, "kill -9 $$").ProcessState}, _Remains{stderr: silent, killCause: ours, limit: 64 << 20}, false, ours.Error()},
		"a foreign kill under pressure":       {_Collected{state: exited(t, "kill -9 $$").ProcessState}, _Remains{stderr: marked, limit: 64 << 20}, true, "near the memory limit of 67108864 bytes"},
		"a foreign kill without pressure":     {_Collected{state: exited(t, "kill -9 $$").ProcessState}, _Remains{stderr: silent, limit: 64 << 20}, false, "evaluator died (signal: killed): killed from outside jaml"},
		"a plain exit names its status":       {_Collected{state: exited(t, "exit 3").ProcessState}, _Remains{stderr: silent, limit: 64 << 20}, false, "evaluator died (exit status 3)"},
		"a pressure marker is not a reason":   {_Collected{state: exited(t, "exit 5").ProcessState}, _Remains{stderr: marked, limit: 64 << 20}, false, "evaluator died (exit status 5)"},
		"an uncollected exit names its wait":  {_Uncollected{waited: syscall.ECHILD}, _Remains{stderr: silent, limit: 64 << 20}, false, "evaluator ended but its exit could not be collected: no child processes"},
		"an uncollected exit after our kill":  {_Uncollected{waited: syscall.ECHILD}, _Remains{stderr: silent, killCause: ours, limit: 64 << 20}, false, ours.Error()},
		"an uncollected exit keeps its panic": {_Uncollected{waited: syscall.ECHILD}, _Remains{stderr: panicked, limit: 64 << 20}, false, "could not be collected (no child processes): panic: boom"},
		"an uncollected exit under pressure":  {_Uncollected{waited: syscall.ECHILD}, _Remains{stderr: marked, limit: 64 << 20}, true, "near the memory limit of 67108864 bytes"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var verdict = c.ending.verdict(c.remains)
			if errors.Is(verdict, MemoryLimit) != c.memory || !strings.Contains(verdict.Error(), c.want) {
				t.Fatalf("got %v (memory %v), want %q (memory %v)", verdict, errors.Is(verdict, MemoryLimit), c.want, c.memory)
			}
		})
	}
}
