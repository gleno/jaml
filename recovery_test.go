//go:build unix && !ios

package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type _RequestLog struct {
	mu     sync.Mutex
	seen   []_Logged
	builds map[_BuiltIn]string
}

type _Logged struct {
	life  int
	asked _Request
	size  int
}

type _BuiltIn struct {
	life int
	job  uint64
}

func (l *_RequestLog) note(entry _Logged) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, entry)
	if entry.asked.Op == opBuild {
		l.builds[_BuiltIn{life: entry.life, job: entry.asked.Job}] = entry.asked.Plan.Written
	}
}

func (l *_RequestLog) served(text string) []_Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []_Request
	for _, entry := range l.seen {
		if entry.asked.Op == opServe && l.builds[_BuiltIn{life: entry.life, job: entry.asked.Unit}] == text {
			found = append(found, entry.asked)
		}
	}
	return found
}

func (l *_RequestLog) lastServed() _Logged {
	l.mu.Lock()
	defer l.mu.Unlock()
	for index := len(l.seen) - 1; index >= 0; index-- {
		if l.seen[index].asked.Op == opServe {
			return l.seen[index]
		}
	}
	return _Logged{}
}

func (l *_RequestLog) count(op _Opcode) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var counted = 0
	for _, entry := range l.seen {
		if entry.asked.Op == op {
			counted++
		}
	}
	return counted
}

func recordRequests(t *testing.T, sb *Sandbox) *_RequestLog {
	t.Helper()
	var log = &_RequestLog{builds: map[_BuiltIn]string{}}
	tap(t, currentChild(t, sb), log)
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	var start = d.start
	d.start = func() (*_Life, error) {
		started, err := start()
		if err == nil {
			tap(t, started, log)
		}
		return started, err
	}
	return log
}

func tap(t *testing.T, child *_Life, log *_RequestLog) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(reader, writer) })
	child.wire.mu.Lock()
	log.mu.Lock()
	for u, r := range child.wire.residents {
		log.builds[_BuiltIn{life: child.cmd.Process.Pid, job: r.build}] = u.plan.Written
	}
	log.mu.Unlock()
	child.wire.mu.Unlock()
	child.wire.writer <- struct{}{}
	var forwarded = child.wire.requests
	child.wire.requests = writer
	<-child.wire.writer
	go func() {
		var frames = _Frames{limit: 1 << 40}.requests(reader)
		for {
			frame, err := frames.next()
			if err != nil {
				return
			}
			var asked _Request
			if json.Unmarshal(frame, &asked) == nil {
				log.note(_Logged{life: child.cmd.Process.Pid, asked: asked, size: len(frame)})
			}
			if _, err := forwarded.Write(headed(frame)); err != nil {
				return
			}
		}
	}()
}

func unitOf(compiled *Expression) string {
	return compiled.held.keep.unit.(*_RemoteUnit).plan.Written
}

func expectSharingAgain(t *testing.T, sb *Sandbox, compiled *Expression, jobs int) {
	t.Helper()
	var peak = watchPeak(sb)
	callSlowly(t, compiled, jobs, 1)
	expectEqual(t, peak(), jobs)
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	expectEqual(t, d.mode, sharing)
}

func TestAMemoryBombAmongEightFailsAloneAndTheOthersReturn(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(8))
	var peers = mustExpression(t, sb, slowCall)
	var bomb = mustExpression(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return 'x'.repeat(1e9).length }")
	var log = recordRequests(t, sb)
	var before = childPID(t, sb)
	var peak = watchPeak(sb)
	var group sync.WaitGroup
	for job := range 7 {
		group.Go(func() {
			var tag int
			if err := resolveInto(within(t, 60*time.Second), &tag, peers, map[string]any{"loops": slowLoops(1), "tag": job}); err != nil || tag != job {
				t.Errorf("peer %d: got %d, %v", job, tag, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	var out any
	var err = resolveInto(within(t, 60*time.Second), &out, bomb, map[string]any{"loops": slowLoops(0.05)})
	group.Wait()
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
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(2))
	var holder = mustExpression(t, sb, "x => { const held = 'x'.repeat(40 << 20); let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return held.length + x.tag }")
	var before = childPID(t, sb)
	var group sync.WaitGroup
	for job := range 2 {
		group.Go(func() {
			var length int
			if err := resolveInto(within(t, 60*time.Second), &length, holder, map[string]any{"loops": slowLoops(2), "tag": job}); err != nil || length != 40<<20+job {
				t.Errorf("holder %d: got %d, %v", job, length, err)
			}
		})
	}
	group.Wait()
	if childPID(t, sb) == before {
		t.Fatal("80 MiB held at once fit under a 64 MiB ceiling")
	}
	expectSharingAgain(t, sb, mustExpression(t, sb, slowCall), 2)
}

func TestARunawayBuiltInPastItsDeadlineFailsAloneWithItsCause(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(8))
	var peers = mustExpression(t, sb, slowCall)
	var runaway = mustExpression(t, sb, "/^(a+)+(?=b)/.test('a'.repeat(32) + 'c')")
	var before = childPID(t, sb)
	var group sync.WaitGroup
	for job := range 7 {
		group.Go(func() {
			var tag int
			if err := resolveInto(within(t, 60*time.Second), &tag, peers, map[string]any{"loops": slowLoops(2.5), "tag": job}); err != nil || tag != job {
				t.Errorf("peer %d: got %d, %v", job, tag, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	var started = time.Now()
	var out any
	var err = resolveInto(within(t, 100*time.Millisecond), &out, runaway)
	var stopped = time.Since(started)
	group.Wait()
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
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(4))
	var observer = mustExpression(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return [Date.now(), Math.random(), Math.random()] }")
	var bomb = mustExpression(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return 'x'.repeat(1e9).length }")
	var log = recordRequests(t, sb)
	var fixed = WithRandomSeed(WithFixedClock(t.Context(), time.UnixMilli(1_700_000_000_000)), 7)
	var contexts = map[string]context.Context{"unset": t.Context(), "fixed": fixed}
	var seen = map[string][]any{}
	var guard sync.Mutex
	var group sync.WaitGroup
	for name, ctx := range contexts {
		group.Go(func() {
			bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			var drawn []any
			if err := resolveInto(bounded, &drawn, observer, map[string]any{"loops": slowLoops(1)}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			guard.Lock()
			seen[name] = drawn
			guard.Unlock()
		})
	}
	time.Sleep(20 * time.Millisecond)
	var out any
	expectError(t, resolveInto(within(t, 60*time.Second), &out, bomb, map[string]any{"loops": slowLoops(0.05)}), MemoryLimit)
	group.Wait()
	var attempts = log.served(unitOf(observer))
	expectEqual(t, len(attempts), 4)
	var pins = map[_Pinned]int{}
	for _, attempt := range attempts {
		pins[attempt.Work.Pinned]++
	}
	expectEqual(t, len(pins), 2)
	for pin, count := range pins {
		expectEqual(t, count, 2)
		if pin.Millis != int64(seen["unset"][0].(float64)) && pin.Millis != int64(seen["fixed"][0].(float64)) {
			t.Fatalf("no Call saw the pinned clock %d: %v", pin.Millis, seen)
		}
	}
	var trusted = mustExpression(t, mustSandbox(t, trustedKind), "x => [Date.now(), Math.random(), Math.random()]")
	var expected []any
	if err := resolveInto(fixed, &expected, trusted, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, seen["fixed"], expected)
}

func TestAnEvalIsOneRequest(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "a: ${ n }\nb: \"${{ n + 1 }} and ${{ n + 2 }}\"\nc: [\"${ n * 2 }\", plain]\n")
	var log = recordRequests(t, sb)
	for n := range 3 {
		var got = isolatedEvalAs[map[string]any](t, document, map[string]any{"n": n})
		expectEqual(t, got["a"], float64(n))
	}
	expectEqual(t, log.count(opServe), 3)
	expectEqual(t, log.count(opBuild), 0)
}

func TestARespawnRebuildsLazily(t *testing.T) {
	var sb = mustIsolated(t)
	var used = mustJaml(t, sb, "v: ${ 1 }\n")
	var idle = mustJaml(t, sb, "v: ${ 2 }\n")
	var log = recordRequests(t, sb)
	killChild(t, sb)
	expectEqual(t, isolatedEvalAs[int](t, used, nil, "v"), 1)
	expectEqual(t, isolatedEvalAs[int](t, used, nil, "v"), 1)
	expectEqual(t, log.count(opBuild), 1)
	expectEqual(t, isolatedEvalAs[int](t, idle, nil, "v"), 2)
	expectEqual(t, log.count(opBuild), 2)
}
