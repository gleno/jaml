//go:build unix && !ios

package jaml

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type requestLog struct {
	mu     sync.Mutex
	seen   []logged
	builds map[builtIn]string
}

type logged struct {
	life  int
	asked request
}

type builtIn struct {
	life int
	job  uint64
}

func (l *requestLog) note(entry logged) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, entry)
	if entry.asked.Op == opBuild {
		l.builds[builtIn{life: entry.life, job: entry.asked.Job}] = entry.asked.Plan.Written
	}
}

func (l *requestLog) served(text string) []request {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []request
	for _, entry := range l.seen {
		if entry.asked.Op == opServe && l.builds[builtIn{life: entry.life, job: entry.asked.Unit}] == text {
			found = append(found, entry.asked)
		}
	}
	return found
}

func (l *requestLog) count(op opcode) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	counted := 0
	for _, entry := range l.seen {
		if entry.asked.Op == op {
			counted++
		}
	}
	return counted
}

func recordRequests(t *testing.T, sb *Sandbox) *requestLog {
	t.Helper()
	log := &requestLog{builds: map[builtIn]string{}}
	tap(t, currentChild(t, sb), log)
	d := dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	start := d.start
	d.start = func() (*life, error) {
		started, err := start()
		if err == nil {
			tap(t, started, log)
		}
		return started, err
	}
	return log
}

func tap(t *testing.T, child *life, log *requestLog) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(read, write) })
	child.wire.mu.Lock()
	log.mu.Lock()
	for u, r := range child.wire.residents {
		log.builds[builtIn{life: child.cmd.Process.Pid, job: r.build}] = u.plan.Written
	}
	log.mu.Unlock()
	child.wire.mu.Unlock()
	child.wire.writer <- struct{}{}
	forwarded := child.wire.requests
	child.wire.requests = write
	<-child.wire.writer
	go func() {
		lines := bufio.NewReader(read)
		for {
			line, err := lines.ReadBytes('\n')
			if err != nil {
				return
			}
			var asked request
			if json.Unmarshal(line, &asked) == nil {
				log.note(logged{life: child.cmd.Process.Pid, asked: asked})
			}
			if _, err := forwarded.Write(line); err != nil {
				return
			}
		}
	}()
}

func unitOf(compiled *Expression) string {
	return compiled.held.unit.(*remoteUnit).plan.Written
}

func expectSharingAgain(t *testing.T, sb *Sandbox, compiled *Expression, jobs int) {
	t.Helper()
	peak := watchPeak(sb)
	callSlowly(t, compiled, jobs, 1)
	expectEqual(t, peak(), jobs)
	d := dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	expectEqual(t, d.mode, sharing)
}

func TestAMemoryBombAmongEightFailsAloneAndTheOthersReturn(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(8))
	peers := mustCompile(t, sb, slowCall)
	bomb := mustCompile(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return 'x'.repeat(1e9).length }")
	log := recordRequests(t, sb)
	before := childPID(t, sb)
	peak := watchPeak(sb)
	var wait sync.WaitGroup
	for job := range 7 {
		wait.Go(func() {
			var tag int
			if err := peers.Call(within(t, 60*time.Second), &tag, map[string]any{"loops": slowLoops(1), "tag": job}); err != nil || tag != job {
				t.Errorf("peer %d: got %d, %v", job, tag, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	var out any
	err := bomb.Call(within(t, 60*time.Second), &out, map[string]any{"loops": slowLoops(0.05)})
	wait.Wait()
	expectError(t, err, MemoryLimit, "$: exceeded the memory limit of 67108864 bytes")
	expectEqual(t, peak(), 8)
	expectEqual(t, len(log.served(unitOf(bomb))), 2)
	expectEqual(t, len(log.served(unitOf(peers))), 14)
	if childPID(t, sb) == before {
		t.Fatal("the bomb did not take its evaluator down")
	}
	expectSharingAgain(t, sb, peers, 8)
}

func TestJobsThatOnlyOverflowTogetherSucceedOnReplay(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(2))
	holder := mustCompile(t, sb, "x => { const held = 'x'.repeat(40 << 20); let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return held.length + x.tag }")
	before := childPID(t, sb)
	var wait sync.WaitGroup
	for job := range 2 {
		wait.Go(func() {
			var length int
			if err := holder.Call(within(t, 60*time.Second), &length, map[string]any{"loops": slowLoops(2), "tag": job}); err != nil || length != 40<<20+job {
				t.Errorf("holder %d: got %d, %v", job, length, err)
			}
		})
	}
	wait.Wait()
	if childPID(t, sb) == before {
		t.Fatal("80 MiB held at once fit under a 64 MiB ceiling")
	}
	expectSharingAgain(t, sb, mustCompile(t, sb, slowCall), 2)
}

func TestARunawayBuiltInPastItsDeadlineFailsAloneWithItsCause(t *testing.T) {
	sb := mustIsolated(t, WithMaxConcurrency(8))
	peers := mustCompile(t, sb, slowCall)
	runaway := mustCompile(t, sb, "/^(a+)+(?=b)/.test('a'.repeat(32) + 'c')")
	before := childPID(t, sb)
	var wait sync.WaitGroup
	for job := range 7 {
		wait.Go(func() {
			var tag int
			if err := peers.Call(within(t, 60*time.Second), &tag, map[string]any{"loops": slowLoops(2.5), "tag": job}); err != nil || tag != job {
				t.Errorf("peer %d: got %d, %v", job, tag, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	var out any
	err := runaway.Eval(within(t, 100*time.Millisecond), &out, nil)
	stopped := time.Since(started)
	wait.Wait()
	expectError(t, err, EvaluationFailed, "$: cancelled: context deadline exceeded")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in %v", err)
	}
	if stopped > 2*time.Second {
		t.Fatalf("a runaway built-in held its caller for %s past a 100ms deadline", stopped)
	}
	if childPID(t, sb) == before {
		t.Fatal("the runaway built-in was never killed")
	}
	expectSharingAgain(t, sb, peers, 8)
}

func TestPinnedClockAndSeedHoldAcrossAReplay(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(4))
	observer := mustCompile(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return [Date.now(), Math.random(), Math.random()] }")
	bomb := mustCompile(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return 'x'.repeat(1e9).length }")
	log := recordRequests(t, sb)
	fixed := WithRandomSeed(WithFixedClock(t.Context(), time.UnixMilli(1_700_000_000_000)), 7)
	contexts := map[string]context.Context{"unset": t.Context(), "fixed": fixed}
	seen := map[string][]any{}
	var guard sync.Mutex
	var wait sync.WaitGroup
	for name, ctx := range contexts {
		wait.Go(func() {
			bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			var drawn []any
			if err := observer.Call(bounded, &drawn, map[string]any{"loops": slowLoops(1)}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			guard.Lock()
			seen[name] = drawn
			guard.Unlock()
		})
	}
	time.Sleep(20 * time.Millisecond)
	var out any
	expectError(t, bomb.Call(within(t, 60*time.Second), &out, map[string]any{"loops": slowLoops(0.05)}), MemoryLimit)
	wait.Wait()
	attempts := log.served(unitOf(observer))
	expectEqual(t, len(attempts), 4)
	pins := map[pinned]int{}
	for _, attempt := range attempts {
		pins[attempt.Work.Pinned]++
	}
	expectEqual(t, len(pins), 2)
	for pin, count := range pins {
		expectEqual(t, count, 2)
		if pin.Clock.UnixMilli() != int64(seen["unset"][0].(float64)) && pin.Clock.UnixMilli() != int64(seen["fixed"][0].(float64)) {
			t.Fatalf("no Call saw the pinned clock %s: %v", pin.Clock, seen)
		}
	}
	trusted := mustCompile(t, mustSandbox(t, trustedKind), "x => [Date.now(), Math.random(), Math.random()]")
	var expected []any
	if err := trusted.Call(fixed, &expected, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, seen["fixed"], expected)
}

func TestAnEvalIsOneRequest(t *testing.T) {
	sb := mustIsolated(t)
	template := mustTemplate(t, sb, "a: ${ n }\nb: \"${{ n + 1 }} and ${{ n + 2 }}\"\nc: [\"${ n * 2 }\", plain]\n")
	log := recordRequests(t, sb)
	for n := range 3 {
		got := isolatedEvalAs[map[string]any](t, template, map[string]any{"n": n})
		expectEqual(t, got["a"], float64(n))
	}
	expectEqual(t, log.count(opServe), 3)
	expectEqual(t, log.count(opBuild), 0)
}

func TestARespawnRebuildsLazily(t *testing.T) {
	sb := mustIsolated(t)
	used := mustTemplate(t, sb, "v: ${ 1 }\n")
	idle := mustTemplate(t, sb, "v: ${ 2 }\n")
	log := recordRequests(t, sb)
	killChild(t, sb)
	expectEqual(t, isolatedEvalAs[int](t, used, nil, "v"), 1)
	expectEqual(t, isolatedEvalAs[int](t, used, nil, "v"), 1)
	expectEqual(t, log.count(opBuild), 1)
	expectEqual(t, isolatedEvalAs[int](t, idle, nil, "v"), 2)
	expectEqual(t, log.count(opBuild), 2)
}
