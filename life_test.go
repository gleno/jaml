//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"fmt"
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
	_, err := MakeSandbox(within(t, 10*time.Second), WithMemoryLimit(panicAtStartLimit))
	expectError(t, err, EvaluationFailed, "starting the evaluator", "evaluator died (exit status 2): panic: an impostor's init")
	if errors.Is(err, Unsupported) {
		t.Fatalf("a child that died before greeting is not a foreign executable: %v", err)
	}
}

func TestACancelledBuildLeavesTheChildAlone(t *testing.T) {
	sb := mustIsolated(t)
	before := childPID(t, sb)
	text := slowBuild(1)
	started := time.Now()
	_, err := sb.MakeTemplate(within(t, 20*time.Millisecond), text)
	expectError(t, err, EvaluationFailed, "building: cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 200*time.Millisecond {
		t.Fatalf("a cancelled build held its caller for %s past a 20ms deadline", waited)
	}
	time.Sleep(time.Second)
	expectEqual(t, isolatedEvalAs[int](t, mustTemplate(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	expectEqual(t, childPID(t, sb), before)
}

func TestACancelledRebuildKeepsItsSlotAndLeavesTheChildAlone(t *testing.T) {
	sb := mustIsolated(t, WithMaxConcurrency(1), WithMemoryLimit(512<<20))
	big := mustTemplate(t, sb, slowBuild(0.4)+"v: ${ 1 }\n")
	small := mustTemplate(t, sb, "v: ${ 2 }\n")
	killChild(t, sb)
	expectEqual(t, isolatedEvalAs[int](t, small, nil, "v"), 2)
	respawned := childPID(t, sb)
	started := time.Now()
	var out int
	expectError(t, big.Eval(within(t, 10*time.Millisecond), &out, nil, "v"), EvaluationFailed, "$.v: cancelled", "deadline exceeded")
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
	sb := mustIsolated(t)
	runaway := mustCompile(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	before := childPID(t, sb)
	started := time.Now()
	var out any
	expectError(t, runaway.Eval(within(t, 20*time.Millisecond), &out, nil), EvaluationFailed, "$: cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 150*time.Millisecond {
		t.Fatalf("a runaway built-in held its caller for %s past a 20ms deadline", waited)
	}
	deadline := time.Now().Add(5 * time.Second)
	for childPID(t, sb) == before {
		if time.Now().After(deadline) {
			t.Fatal("the runaway built-in was never killed")
		}
		expectEqual(t, isolatedEvalAs[int](t, mustTemplate(t, sb, "v: ${ 1 }\n"), nil, "v"), 1)
		time.Sleep(20 * time.Millisecond)
	}
}

func crowdedTemplate(index int) string {
	var written strings.Builder
	padding := strings.Repeat("p", 400)
	for key := range 9500 {
		fmt.Fprintf(&written, "k%d: \"%s%d-%d\"\n", key, padding, index, key)
	}
	written.WriteString("e: ${ x + 1 }\n")
	return written.String()
}

func TestJobsThatOverflowTogetherInACrowdSucceedOnReplay(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(24<<20), WithMaxConcurrency(8), WithBuildTimeout(30*time.Second))
	templates := make([]*Template, 8)
	for index := range templates {
		templates[index] = mustTemplate(t, sb, crowdedTemplate(index))
	}
	for round := range 2 {
		var wait sync.WaitGroup
		for index, template := range templates {
			wait.Go(func() {
				var out int
				if err := template.Eval(within(t, 120*time.Second), &out, map[string]any{"x": index}, "e"); err != nil || out != index+1 {
					t.Errorf("round %d, template %d: got %d, %v", round, index, out, err)
				}
			})
		}
		wait.Wait()
	}
}

func TestACancelBehindABigRequestStillLands(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(512<<20), WithMaxConcurrency(4))
	spin := mustTemplate(t, sb, "spin: ${ (() => { while (true) {} })() }\n")
	before := childPID(t, sb)
	huge := "v: '" + strings.Repeat("\x01", (16<<20)/raceSlowdown) + "'\n"
	spinning := make(chan error, 1)
	go func() {
		var out any
		spinning <- spin.Eval(within(t, 150*time.Millisecond), &out, nil, "spin")
	}()
	time.Sleep(50 * time.Millisecond)
	_, err := sb.MakeTemplate(within(t, 60*time.Second), huge)
	expectError(t, err, InvalidTemplate)
	expectError(t, <-spinning, EvaluationFailed, "$.spin: cancelled")
	expectEqual(t, childPID(t, sb), before)
}

func TestForgottenRebuildsLeaveNothingResident(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20), WithBuildTimeout(30*time.Second))
	templates := make([]*Template, 12/min(raceSlowdown, 3))
	ends := make([]context.CancelFunc, len(templates))
	var half time.Duration
	for index := range templates {
		ctx, end := context.WithCancel(t.Context())
		started := time.Now()
		made, err := sb.MakeTemplate(ctx, listOfPlainServers(30000)+fmt.Sprintf("v: %d\n", index))
		if err != nil {
			t.Fatal(err)
		}
		half = max(half, time.Since(started)/2)
		templates[index], ends[index] = made, end
		killChild(t, sb)
	}
	expectEqual(t, isolatedEvalAs[int](t, mustTemplate(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	before := childPID(t, sb)
	for index, template := range templates {
		time.AfterFunc(half, ends[index])
		var out int
		expectError(t, template.Eval(t.Context(), &out, nil, "v"), Closed)
		time.Sleep(2 * half)
	}
	expectEqual(t, isolatedEvalAs[int](t, mustTemplate(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	expectEqual(t, childPID(t, sb), before)
}

func TestACallCancelledWhileQueuedNamesItsPath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 }\nlist: [\"${ 1 }\"]\n")
		spinning := make(chan error, 1)
		go func() {
			var out any
			spinning <- template.Eval(within(t, time.Second), &out, nil, "spin")
		}()
		time.Sleep(100 * time.Millisecond)
		expectError(t, evalBounded(t, template, within(t, 100*time.Millisecond), "ok"), EvaluationFailed, "$.ok: cancelled")
		var out any
		expectError(t, template.Eval(within(t, 100*time.Millisecond), &out, nil, "list", "0"), EvaluationFailed, "$.list[0]: cancelled")
		<-spinning
	}, WithMaxConcurrency(1))
}

func TestAFailingRespawnFailsEveryQueuedJobOnce(t *testing.T) {
	sb := mustIsolated(t, WithMaxConcurrency(8))
	template := mustTemplate(t, sb, "v: ${ 1 }\n")
	respawnAs(t, sb, childSpec{limit: silentLimit, buildTimeout: 300 * time.Millisecond})
	killChild(t, sb)
	started := time.Now()
	failures := make(chan error, 6)
	for range 6 {
		go func() {
			var out int
			failures <- template.Eval(within(t, 30*time.Second), &out, nil, "v")
		}()
	}
	var first error
	for range 6 {
		err := <-failures
		expectError(t, err, EvaluationFailed, "starting the evaluator", "took longer than 300ms to start")
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
	sb := mustIsolated(t)
	process := currentChild(t, sb)
	pid := process.cmd.Process.Pid
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := process.send(t.Context(), request{Op: opBuild, Plan: &blueprint{Written: strings.Repeat("x", 4<<20)}})
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
