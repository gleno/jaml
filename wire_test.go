//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func unitOn(t *testing.T, text string) *remoteUnit {
	t.Helper()
	made, _ := endingUnitOn(t, text)
	return made
}

func endingUnitOn(t *testing.T, text string) (*remoteUnit, context.CancelFunc) {
	t.Helper()
	ctx, end := context.WithCancel(t.Context())
	t.Cleanup(end)
	return &remoteUnit{plan: blueprint{Written: text, Null: Loose, Undefined: Strict}, lifetime: ctx}, end
}

func ignoreEarly(outcome) {}

func residentBuild(t *testing.T, child *life, u *remoteUnit) uint64 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r := residencyOf(child, u); r != nil {
			child.wire.mu.Lock()
			build := r.build
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

func expectMissing(t *testing.T, child *life, build uint64) {
	t.Helper()
	served := child.serve(within(t, 10*time.Second), build, work{})
	if failed, isRefused := served.(refused); !isRefused || !errors.Is(failed.err, errMissing) {
		t.Fatalf("the evaluator still serves build %d: %v", build, served)
	}
}

func expectServes(t *testing.T, child *life, build uint64, want string) {
	t.Helper()
	served := child.serve(within(t, 10*time.Second), build, work{Path: []string{"v"}})
	data, isAnswered := served.(answered)
	if !isAnswered || string(data.data) != want {
		t.Fatalf("build %d served %v, want %s", build, served, want)
	}
}

func deliver(t *testing.T, child *life, outgoing request) {
	t.Helper()
	select {
	case <-child.tell(outgoing):
	case <-time.After(10 * time.Second):
		t.Fatal("a control message was never delivered")
	}
}

func TestAForgetDuringItsBuildLeavesNothingServable(t *testing.T) {
	child := currentChild(t, mustIsolated(t, WithMemoryLimit(512<<20)))
	u, end := endingUnitOn(t, slowBuild(0.5)+"v: 1\n")
	settled := make(chan *residency, 1)
	go func() { settled <- child.reside(u, ignoreEarly) }()
	build := residentBuild(t, child, u)
	time.Sleep(20 * time.Millisecond)
	end()
	r := <-settled
	if _, built := r.result.(answered); built {
		time.Sleep(50 * time.Millisecond)
	}
	expectMissing(t, child, build)
	if residencyOf(child, u) != nil {
		t.Fatal("an ended unit is still in the ledger")
	}
}

func TestAForgetThatOvertakesItsBuildIsHonoured(t *testing.T) {
	child := currentChild(t, mustIsolated(t))
	child.wire.writer <- struct{}{}
	next := child.wire.sent + 1
	<-child.wire.writer
	deliver(t, child, request{Op: opForget, Unit: next})
	u := unitOn(t, "v: 1\n")
	r := child.reside(u, ignoreEarly)
	expectEqual(t, r.build, next)
	if failed, isRefused := r.result.(refused); !isRefused || !strings.Contains(failed.err.Error(), "cancelled") {
		t.Fatalf("a build forgotten before it arrived was answered %v", r.result)
	}
	expectMissing(t, child, next)
	expectServes(t, child, child.reside(unitOn(t, "v: 2\n"), ignoreEarly).build, "2")
}

func TestARebuildSurvivesAStaleForget(t *testing.T) {
	child := currentChild(t, mustIsolated(t))
	u := unitOn(t, "v: 1\n")
	first := child.reside(u, ignoreEarly)
	child.drop(first)
	child.wire.mu.Lock()
	rebuilt := make(chan *residency, 1)
	go func() { rebuilt <- child.reside(u, ignoreEarly) }()
	time.Sleep(20 * time.Millisecond)
	deliver(t, child, request{Op: opForget, Unit: first.build})
	child.wire.mu.Unlock()
	second := <-rebuilt
	deliver(t, child, request{Op: opForget, Unit: first.build})
	if second.build == first.build {
		t.Fatalf("a rebuild reused build id %d", first.build)
	}
	expectServes(t, child, second.build, "1")
	expectMissing(t, child, first.build)
}

func TestAStaleDropLeavesANewerResidency(t *testing.T) {
	child := currentChild(t, mustIsolated(t))
	u := unitOn(t, "v: 1\n")
	older := child.reside(u, ignoreEarly)
	child.drop(older)
	newer := child.reside(u, ignoreEarly)
	child.drop(older)
	if residencyOf(child, u) != newer {
		t.Fatal("dropping an older residency removed the newer one")
	}
}

func TestAUnitTheChildLostIsBuiltAgain(t *testing.T) {
	sb := mustIsolated(t)
	document := mustJaml(t, sb, "v: ${ 4 }\n")
	child := currentChild(t, sb)
	build := residentBuild(t, child, document.held.keep.unit.(*remoteUnit))
	deliver(t, child, request{Op: opForget, Unit: build})
	expectMissing(t, child, build)
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 4)
	expectEqual(t, currentChild(t, sb), child)
}

func exited(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	_ = cmd.Run()
	return cmd
}

func TestADeathIsClassifiedFromTheEvidence(t *testing.T) {
	ours := errors.New("cancelled: context deadline exceeded")
	marked := &headBuffer{limit: stderrHeadBytes}
	_, _ = marked.Write([]byte(pressureMarker + "\n"))
	panicked := &headBuffer{limit: stderrHeadBytes}
	_, _ = panicked.Write([]byte(pressureMarker + "\npanic: boom\n\ngoroutine 1 [running]:\n"))
	silent := &headBuffer{limit: stderrHeadBytes}
	cases := map[string]struct {
		ending exit
		memory bool
		want   string
	}{
		"an exit 86 beats our kill":         {exit{state: exited(t, "exit 86").ProcessState, stderr: silent, killedFor: ours, limit: 64 << 20}, true, "exceeded the memory limit of 67108864 bytes"},
		"a panic beats our kill":            {exit{state: exited(t, "exit 2").ProcessState, stderr: panicked, killedFor: ours, limit: 64 << 20}, false, "evaluator died (exit status 2): panic: boom"},
		"our kill names its reason":         {exit{state: exited(t, "kill -9 $$").ProcessState, stderr: silent, killedFor: ours, limit: 64 << 20}, false, ours.Error()},
		"a foreign kill under pressure":     {exit{state: exited(t, "kill -9 $$").ProcessState, stderr: marked, limit: 64 << 20}, true, "near the memory limit of 67108864 bytes"},
		"a foreign kill without pressure":   {exit{state: exited(t, "kill -9 $$").ProcessState, stderr: silent, limit: 64 << 20}, false, "evaluator died (signal: killed): killed from outside jaml"},
		"a plain exit names its status":     {exit{state: exited(t, "exit 3").ProcessState, stderr: silent, limit: 64 << 20}, false, "evaluator died (exit status 3)"},
		"a pressure marker is not a reason": {exit{state: exited(t, "exit 5").ProcessState, stderr: marked, limit: 64 << 20}, false, "evaluator died (exit status 5)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			verdict := c.ending.verdict()
			if errors.Is(verdict, MemoryLimit) != c.memory || !strings.Contains(verdict.Error(), c.want) {
				t.Fatalf("got %v (memory %v), want %q (memory %v)", verdict, errors.Is(verdict, MemoryLimit), c.want, c.memory)
			}
		})
	}
}
