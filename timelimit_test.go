package jaml

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const busyMillisecond = "busy: ${ (() => { let n = 0; for (let i = 0; i < 5e4; i++) { n += i % 7 } return n })() }\nquick: ${ 1 + 1 }\n"

func TestARunPastItsCPULimitFailsWithCPULimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var started = time.Now()
		var out any
		var err = resolveInto(WithTimeLimit(t.Context(), 100*time.Millisecond), &out, document.At("spin"))
		expectError(t, err, TimeLimit, "$.spin: ${(() => { while (true) {} })()}: exceeded the time limit of 100ms: evaluation failed: expression error")
		expectError(t, err, EvaluationFailed)
		expectFailure(t, err, "$.spin", "exceeded the time limit of 100ms")
		for _, foreign := range []error{WallLimit, context.DeadlineExceeded, context.Canceled} {
			if errors.Is(err, foreign) {
				t.Fatalf("a CPU time limit looked like %v: %v", foreign, err)
			}
		}
		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("a 100ms CPU time limit held its caller for %s", waited)
		}
		expectEqual(t, mustResolve[int](t, WithTimeLimit(t.Context(), time.Second), document.At("quick")), 2)
	})
}

func TestEachLimitKeepsItsOwnCause(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var spin = mustJaml(t, sb, spinning).At("spin")
		var out any
		var wallFirst = WithTimeLimit(WithWallLimit(t.Context(), 50*time.Millisecond), 10*time.Second)
		var err = resolveInto(wallFirst, &out, spin)
		expectError(t, err, WallLimit, "exceeded the wall time limit of 50ms")
		if errors.Is(err, TimeLimit) {
			t.Fatalf("a wall time limit was blamed on the CPU budget: %v", err)
		}
		var cpuFirst = WithTimeLimit(WithWallLimit(t.Context(), 10*time.Second), 50*time.Millisecond)
		err = resolveInto(cpuFirst, &out, spin)
		expectError(t, err, TimeLimit, "exceeded the time limit of 50ms")
		if errors.Is(err, WallLimit) {
			t.Fatalf("a CPU budget was blamed on the wall time limit: %v", err)
		}
	})
}

func TestTheSmallestCPULimitWins(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var spin = mustJaml(t, sb, spinning).At("spin")
		var out any
		var inner = WithTimeLimit(WithTimeLimit(t.Context(), 50*time.Millisecond), time.Minute)
		expectError(t, resolveInto(inner, &out, spin), TimeLimit, "exceeded the time limit of 50ms")
		var outer = WithTimeLimit(WithTimeLimit(t.Context(), time.Minute), 60*time.Millisecond)
		expectError(t, resolveInto(outer, &out, spin), TimeLimit, "exceeded the time limit of 60ms")
	})
}

func TestANonPositiveCPULimitIsRefused(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithTimeLimit(t.Context(), 0), &out, mustJaml(t, sb, spinning).At("quick"))
		expectError(t, err, InvalidInput, "$: a time limit must be positive, got 0s")
	})
}

type _MeterLog struct {
	mu   sync.Mutex
	seen []Usage
}

func (l *_MeterLog) record(used Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, used)
}

func (l *_MeterLog) only(t *testing.T) Usage {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) != 1 {
		t.Fatalf("the meter was called %d times, want once: %v", len(l.seen), l.seen)
	}
	return l.seen[0]
}

func (l *_MeterLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}

func metered(t *testing.T, ctx context.Context) (context.Context, *_MeterLog) {
	t.Helper()
	var log = &_MeterLog{}
	return WithMeter(ctx, log.record), log
}

func expectPlausible(t *testing.T, used Usage) {
	t.Helper()
	if used.Wall <= 0 || used.CPU < 0 {
		t.Fatalf("implausible usage %+v", used)
	}
	if used.CPU > used.Wall+5*time.Millisecond {
		t.Fatalf("a single thread used %s of CPU in %s of wall time", used.CPU, used.Wall)
	}
}

func TestAResolveReportsTheTimeItUsed(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, busyMillisecond)
		ctx, meter := metered(t, t.Context())
		if got := mustResolve[int](t, ctx, document.At("busy")); got <= 0 {
			t.Fatalf("busy produced %d", got)
		}
		var used = meter.only(t)
		expectPlausible(t, used)
		if goruntime.GOOS == "linux" || goruntime.GOOS == "darwin" {
			if used.CPU < 100*time.Microsecond {
				t.Fatalf("a job that loops 50000 times reported only %s of CPU", used.CPU)
			}
		}
		ctx, meter = metered(t, t.Context())
		expectEqual(t, mustResolve[int](t, ctx, document.At("quick")), 2)
		expectPlausible(t, meter.only(t))
	})
}

func TestAFailedResolveStillReportsTheTimeItUsed(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var spin = mustJaml(t, sb, spinning).At("spin")
		ctx, meter := metered(t, WithTimeLimit(t.Context(), 50*time.Millisecond))
		var out any
		expectError(t, resolveInto(ctx, &out, spin), TimeLimit)
		var used = meter.only(t)
		expectPlausible(t, used)
		if goruntime.GOOS == "linux" && used.CPU < 40*time.Millisecond {
			t.Fatalf("a job stopped by a 50ms CPU budget reported only %s of CPU", used.CPU)
		}
		var thrower = mustExpression(t, sb, "(() => { throw new Error('boom') })()")
		ctx, meter = metered(t, t.Context())
		expectError(t, resolveInto(ctx, &out, thrower), EvaluationFailed, "boom")
		expectPlausible(t, meter.only(t))
	})
}

func TestAResolveRefusedBeforeRunningReportsNothing(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var quick = mustJaml(t, sb, spinning).At("quick")
		ctx, meter := metered(t, WithTimeLimit(t.Context(), 0))
		var out any
		expectError(t, resolveInto(ctx, &out, quick), InvalidInput)
		ctx, meter2 := metered(t, t.Context())
		expectError(t, resolveInto(ctx, &out, quick, make(chan int)), InvalidInput)
		if meter.count()+meter2.count() != 0 {
			t.Fatalf("a refused call reported usage: %v %v", meter.seen, meter2.seen)
		}
	})
}

func TestAWallLimitedJobWaitingForASlotReportsOnlyItsOwnTime(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, spinning)
		var blocker = make(chan error, 1)
		go func() {
			var out any
			blocker <- resolveInto(within(t, 300*time.Millisecond), &out, document.At("spin"))
		}()
		time.Sleep(100 * time.Millisecond)
		ctx, meter := metered(t, WithTimeLimit(t.Context(), 50*time.Millisecond))
		expectEqual(t, mustResolve[int](t, ctx, document.At("quick")), 2)
		if used := meter.only(t); used.Wall > 100*time.Millisecond {
			t.Fatalf("waiting for a slot was reported as the job's own time: %+v", used)
		}
		expectError(t, <-blocker, EvaluationFailed, "deadline exceeded")
	}, WithMaxConcurrency(1))
}

func TestAHostileBuildIsBoundedByTheBuildCPULimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var started = time.Now()
		_, err := sb.MakeExpressionFromJaml(t.Context(), hostileText())
		expectError(t, err, TimeLimit, "$: the build exceeded its time limit of 100ms")
		if errors.Is(err, WallLimit) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a build past its CPU budget looked like a wall timeout: %v", err)
		}
		if waited := time.Since(started); waited > 3*time.Second {
			t.Fatalf("a hostile build held construction for %s past its 100ms CPU budget", waited)
		}
		expectEqual(t, mustResolve[int](t, t.Context(), mustJaml(t, sb, "v: ${ 6 }\n").At("v")), 6)
	}, WithBuildTimeLimit(100*time.Millisecond))
}

func TestAHostileScriptCompileIsBoundedByTheBuildCPULimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var quick = mustJaml(t, sb, spinning).At("quick")
		var hostile = "const v = [" + strings.Repeat("("+strings.Repeat("-1+", 4095)+"0),", 5) + "]"
		var out any
		var err = resolveInto(WithScript(t.Context(), hostile), &out, quick)
		expectError(t, err, TimeLimit, "the build exceeded its time limit of 100ms")
		expectEqual(t, mustResolve[int](t, t.Context(), quick), 2)
	}, WithBuildTimeLimit(100*time.Millisecond))
}

func TestANonPositiveBuildCPULimitIsRefused(t *testing.T) {
	_, err := MakeTrustedSandbox(t.Context(), WithBuildTimeLimit(0))
	expectError(t, err, InvalidExpression, "build time limit must be positive, got 0s")
}

func TestASiblingBurningCPUNeverChargesAnInnocentJob(t *testing.T) {
	if !threadClockIsReadable() {
		t.Skip("a CPU budget on this platform counts wall time")
	}
	var burners = 2 * goruntime.GOMAXPROCS(0)
	var sb = mustIsolated(t, WithMaxConcurrency(burners+1))
	var document = mustJaml(t, sb, busyMillisecond)
	var spin = mustJaml(t, sb, spinning).At("spin")
	var child = currentChild(t, sb)
	var alone Usage
	for range 5 {
		ctx, meter := metered(t, t.Context())
		mustResolve[int](t, ctx, document.At("busy"))
		if used := meter.only(t); used.CPU > alone.CPU {
			alone = used
		}
	}
	var burned = make(chan error, burners)
	for range burners {
		go func() {
			var out any
			burned <- resolveInto(WithWallLimit(t.Context(), time.Second), &out, spin)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	var budget = 10*alone.CPU + 50*time.Millisecond
	var slowest, costliest Usage
	for range 20 {
		ctx, meter := metered(t, WithTimeLimit(t.Context(), budget))
		if _, err := Resolve[int](ctx, document.At("busy")); err != nil {
			t.Fatalf("an innocent job that costs %s alone failed its %s budget beside %d CPU burners: %v", alone.CPU, budget, burners, err)
		}
		var used = meter.only(t)
		if used.Wall > slowest.Wall {
			slowest = used
		}
		if used.CPU > costliest.CPU {
			costliest = used
		}
	}
	t.Logf("alone the innocent job used %+v; beside %d burners the slowest used %+v and the costliest %+v, within a budget of %s", alone, burners, slowest, costliest, budget)
	if costliest.CPU > 10*alone.CPU+5*time.Millisecond {
		t.Fatalf("an innocent job was charged %s of CPU beside burners, %s alone", costliest.CPU, alone.CPU)
	}
	var burnersEnded = time.Now()
	for range burners {
		expectError(t, <-burned, WallLimit)
	}
	t.Logf("the burners ended %s after they were due", time.Since(burnersEnded))
	if !child.alive() {
		t.Fatalf("the child died under the burners: %v", child.lost().cause)
	}
}

func threadClockIsReadable() bool {
	var readable bool
	onALockedThread(func() {
		_, readable = currentThreadClock().cpu()
	})
	return readable
}

func TestTheThreadClockFollowsTheLockedThread(t *testing.T) {
	if !threadClockIsReadable() {
		t.Skip("no cross-thread CPU clock on this platform")
	}
	var fromOutside = make(chan time.Duration, 1)
	onALockedThread(func() {
		var meter = startMeter()
		var until = time.Now().Add(30 * time.Millisecond)
		var n = 0
		for time.Now().Before(until) {
			n++
		}
		go func() { fromOutside <- meter.spent() }()
		var outside = <-fromOutside
		var own = meter.used().CPU
		if n == 0 || own < 20*time.Millisecond || outside < 20*time.Millisecond {
			t.Fatalf("30ms of spinning read as %s from its thread and %s from outside", own, outside)
		}
		if outside > own+5*time.Millisecond {
			t.Fatalf("the clock read from outside, %s, ran ahead of the thread's own %s", outside, own)
		}
	})
}

func TestACPULimitArmsNothingWithoutALimit(t *testing.T) {
	var meter = startMeter()
	ctx, release := _TimeLimit(0).enforce(t.Context(), meter, false)
	defer release()
	if ctx != t.Context() {
		t.Fatal("no limit still wrapped the context")
	}
}

func TestARunStuckInABuiltInPastItsCPULimitEndsTheChildAndFailsWithCPULimit(t *testing.T) {
	var sb = mustIsolated(t)
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var child = currentChild(t, sb)
	var out any
	var err = resolveInto(WithTimeLimit(t.Context(), 50*time.Millisecond), &out, runaway)
	expectError(t, err, TimeLimit, "$: /^(?=a)(a+)+$/.test('a'.repeat(30) + 'b'): exceeded the time limit of 50ms and was still inside a built-in 250ms after it was told to stop")
	expectStuckAfterCancel(t, child)
}
