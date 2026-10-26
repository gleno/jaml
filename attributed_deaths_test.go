//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
)

type _TagOfAnErrand struct{}

type _HeardDeath struct {
	tag   any
	cause error
}

type _HeardDeaths struct {
	mu      sync.Mutex
	heard   []_HeardDeath
	arrived chan struct{}
}

func listenForDeaths() (*_HeardDeaths, SandboxOption) {
	var heard = &_HeardDeaths{arrived: make(chan struct{}, 64)}
	return heard, WithAttributedDeaths(func(ctx context.Context, death AttributedDeath) {
		heard.mu.Lock()
		heard.heard = append(heard.heard, _HeardDeath{tag: ctx.Value(_TagOfAnErrand{}), cause: death.Cause})
		heard.mu.Unlock()
		heard.arrived <- struct{}{}
	})
}

func (h *_HeardDeaths) list() []_HeardDeath {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]_HeardDeath{}, h.heard...)
}

func (h *_HeardDeaths) awaitOne(t *testing.T) {
	t.Helper()
	select {
	case <-h.arrived:
	case <-time.After(60 * time.Second):
		t.Fatal("no death was attributed")
	}
}

func tagged(ctx context.Context, tag any) context.Context {
	return context.WithValue(ctx, _TagOfAnErrand{}, tag)
}

const aBombPastSixtyFourMiB = "'x'.repeat(1e9).length"

func TestAMemoryBombAmongEightIsHeardAsTheOneAttributedDeath(t *testing.T) {
	heard, listening := listenForDeaths()
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(8), listening)
	var peers = mustExpression(t, sb, slowCall)
	var bomb = mustExpression(t, sb, "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return 'x'.repeat(1e9).length }")
	var group sync.WaitGroup
	for job := range 7 {
		group.Go(func() {
			var tag int
			if err := resolveInto(tagged(within(t, 60*time.Second), job), &tag, peers, map[string]any{"loops": slowLoops(1), "tag": job}); err != nil || tag != job {
				t.Errorf("peer %d: got %d, %v", job, tag, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	var out any
	var err = resolveInto(tagged(within(t, 60*time.Second), "bomb"), &out, bomb, map[string]any{"loops": slowLoops(0.05)})
	group.Wait()
	expectError(t, err, MemoryLimit)
	heard.awaitOne(t)
	var deaths = heard.list()
	var failure *Failure
	if len(deaths) != 1 || deaths[0].tag != "bomb" || !errors.Is(deaths[0].cause, MemoryLimit) || !errors.As(deaths[0].cause, &failure) || failure.Reason != "exceeded the memory limit of 67108864 bytes" {
		t.Fatalf("the bomb alone is heard, once, as a memory death; heard %+v", deaths)
	}
}

func TestABombWhoseCallerGaveUpBeforeItsReplayIsStillHeard(t *testing.T) {
	heard, listening := listenForDeaths()
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(2), listening)
	var bomb = mustExpression(t, sb, aBombPastSixtyFourMiB)
	var first = currentChild(t, sb)
	asking, giveUp := context.WithCancel(tagged(t.Context(), "bomb-left"))
	defer giveUp()
	go func() {
		<-first.ended
		giveUp()
	}()
	var out any
	var err = resolveInto(asking, &out, bomb)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that gave up is answered cancelled, got %v", err)
	}
	heard.awaitOne(t)
	var deaths = heard.list()
	if len(deaths) != 1 || deaths[0].tag != "bomb-left" || !errors.Is(deaths[0].cause, MemoryLimit) {
		t.Fatalf("the bomb whose caller left is still replayed alone and heard; heard %+v", deaths)
	}
}

func TestASandboxThatListensForNoDeathsDropsASuspectWhoseCallerLeft(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(2))
	var bomb = mustExpression(t, sb, aBombPastSixtyFourMiB)
	var first = currentChild(t, sb)
	asking, giveUp := context.WithCancel(t.Context())
	defer giveUp()
	go func() {
		<-first.ended
		giveUp()
	}()
	var out any
	if err := resolveInto(asking, &out, bomb); !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that gave up is answered cancelled, got %v", err)
	}
	var d = dispatcherOf(t, sb)
	var deadline = time.Now().Add(30 * time.Second)
	for {
		d.mu.Lock()
		var quiet = d.mode == sharing && len(d.suspects) == 0
		d.mu.Unlock()
		if quiet {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the dropped suspect kept the sandbox recovering")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEveryDeathJamlNamesIsMatchedByItsOwnSentinel(t *testing.T) {
	var outside = blameless(_OutsideDeath("evaluator died (signal: killed): killed from outside jaml, often by the kernel's out-of-memory killer"))
	if !errors.Is(outside, KilledFromOutside) || !errors.Is(outside, Unavailable) {
		t.Fatalf("a kill from outside is KilledFromOutside and still Unavailable: %v", outside)
	}
	var stuck error = _StuckDeath("evaluator exited (exit status 3): a job stuck after cancel was still inside a built-in 2s after it was told to stop")
	if !errors.Is(stuck, StuckAfterCancel) || errors.Is(stuck, EvaluatorDied) {
		t.Fatalf("a job stuck after cancel is StuckAfterCancel and no other death: %v", stuck)
	}
	var waited = errors.New("wait: no child processes")
	var uncollected = _Uncollected{waited: waited}.verdict(_Remains{stderr: &_HeadBuffer{limit: 64}})
	if !errors.Is(uncollected, ExitNotCollected) || !errors.Is(uncollected, waited) || uncollected.Error() != "evaluator ended but its exit could not be collected: wait: no child processes" {
		t.Fatalf("an exit jaml could not collect is ExitNotCollected, keeps what the wait said and its words: %v", uncollected)
	}
	for _, sentinel := range []error{KilledFromOutside, StuckAfterCancel, ExitNotCollected} {
		if !errors.Is(sentinel, ExpressionError) {
			t.Fatalf("%v is an ExpressionError", sentinel)
		}
	}
}

func TestAListenerOfAttributedDeathsMustBeGiven(t *testing.T) {
	if _, err := MakeSecureSandbox(t.Context(), WithAttributedDeaths(nil)); !errors.Is(err, InvalidExpression) {
		t.Fatalf("a nil listener is refused, got %v", err)
	}
}

func boundTheReplaysOf(t *testing.T, sb *Sandbox, bound time.Duration) {
	t.Helper()
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replays = bound
}

func TestTheBoundOfAReplayWhoseCallerLeftStartsAtItsAdmissionAlone(t *testing.T) {
	var d = &_Dispatcher{heard: func(context.Context, AttributedDeath) {}, replays: 20 * time.Millisecond}
	caller, cancel := context.WithCancel(t.Context())
	cancel()
	var e = &_Errand{caller: caller}
	d.replayPastTheCaller(e)
	defer e.endTheReplayBound()
	time.Sleep(40 * time.Millisecond)
	if e.context().Err() != nil {
		t.Fatal("a queued replay consumed its allowance")
	}
	d.boundTheReplayFromItsAdmission(e, _Shared{})
	if e.replay.finished != nil {
		t.Fatal("a shared lease started the replay allowance")
	}
	var expired = make(chan struct{})
	e.replay.start(caller, d.replayBound(), func() { close(expired) })
	select {
	case <-expired:
	case <-time.After(time.Second):
		t.Fatal("an admitted abandoned replay did not expire")
	}
	if !e.replay.expired() {
		t.Fatal("the replay did not retain its expiry cause")
	}
}

func TestAReplayWhoseCallerLeftAndThatOutlivesItsBoundIsHeardAsDropped(t *testing.T) {
	heard, listening := listenForDeaths()
	var sb = mustIsolated(t, WithMaxConcurrency(2), listening)
	boundTheReplaysOf(t, sb, 300*time.Millisecond)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var first = currentChild(t, sb)
	asking, giveUp := context.WithCancel(tagged(t.Context(), "spin-left"))
	defer giveUp()
	var spun = make(chan error, 1)
	go func() {
		var out any
		spun <- resolveInto(asking, &out, spin)
	}()
	go func() {
		<-first.ended
		giveUp()
	}()
	killMidJob(t, first)
	if err := <-spun; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that gave up is answered cancelled, got %v", err)
	}
	heard.awaitOne(t)
	var deaths = heard.list()
	if len(deaths) != 1 || deaths[0].tag != "spin-left" || !errors.Is(deaths[0].cause, ReplayDropped) || !errors.Is(deaths[0].cause, Unavailable) {
		t.Fatalf("the dropped replay is heard once as ReplayDropped; heard %+v", deaths)
	}
}

func TestSuspectsWhoseCallersLeftAreReplayedLastAndDroppedOnceTheDeathIsAttributed(t *testing.T) {
	heard, listening := listenForDeaths()
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(2), listening)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var bomb = mustExpression(t, sb, aBombPastSixtyFourMiB)
	var log = recordRequests(t, sb)
	var first = currentChild(t, sb)
	asking, giveUp := context.WithCancel(tagged(t.Context(), "spin-left"))
	defer giveUp()
	var spun = make(chan error, 1)
	go func() {
		var out any
		spun <- resolveInto(asking, &out, spin)
	}()
	go func() {
		<-first.ended
		giveUp()
	}()
	var deadline = time.Now().Add(30 * time.Second)
	for !serving(first) {
		if time.Now().After(deadline) {
			t.Fatal("the spin was never handed to the evaluator")
		}
		time.Sleep(time.Millisecond)
	}
	var out any
	var err = resolveInto(tagged(within(t, 60*time.Second), "bomb"), &out, bomb)
	expectError(t, err, MemoryLimit)
	if err := <-spun; !errors.Is(err, context.Canceled) {
		t.Fatalf("the spin whose caller left is answered cancelled, got %v", err)
	}
	heard.awaitOne(t)
	time.Sleep(200 * time.Millisecond)
	var deaths = heard.list()
	if len(deaths) != 1 || deaths[0].tag != "bomb" || !errors.Is(deaths[0].cause, MemoryLimit) {
		t.Fatalf("only the bomb is heard; heard %+v", deaths)
	}
	if served := len(log.served(unitOf(spin))); served != 1 {
		t.Fatalf("the spin whose caller left was replayed after the death was attributed: served %d times", served)
	}
}

func TestAReplayWithItsCallerPresentOutlivesTheRetentionBound(t *testing.T) {
	heard, listening := listenForDeaths()
	var sb = mustIsolated(t, listening)
	boundTheReplaysOf(t, sb, 100*time.Millisecond)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var log = recordRequests(t, sb)
	asking, cancel := context.WithCancel(t.Context())
	defer cancel()
	var done = make(chan error, 1)
	go func() { _, err := Resolve[any](asking, spin); done <- err }()
	awaitServed(t, log, spin, 1)
	killChild(t, sb)
	awaitServed(t, log, spin, 2)
	select {
	case err := <-done:
		t.Fatalf("a present caller lost its replay to the retention bound: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	if deaths := heard.list(); len(deaths) != 0 {
		t.Fatalf("the present caller was reported dropped: %+v", deaths)
	}
	cancel()
	expectError(t, <-done, context.Canceled)
	heard.awaitOne(t)
	if deaths := heard.list(); len(deaths) != 1 || !errors.Is(deaths[0].cause, ReplayDropped) {
		t.Fatalf("a caller leaving past the bound must drop its replay once: %+v", deaths)
	}
}

func TestAConsoleDoesNotKeepItsCallerWaitingForAnAttributionReplay(t *testing.T) {
	heard, deathsOption := listenForDeaths()
	var sb = mustIsolated(t, deathsOption)
	boundTheReplaysOf(t, sb, 2*time.Second)
	var spin = mustExpression(t, sb, threeThenSpin)
	var log = recordRequests(t, sb)
	asking, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, lines := listening(asking)
	var done = make(chan error, 1)
	go func() { _, err := Resolve[any](ctx, spin); done <- err }()
	awaitServed(t, log, spin, 1)
	awaitConsoleLines(t, lines, len(threeLines))
	killChild(t, sb)
	awaitServed(t, log, spin, 2)
	cancel()
	select {
	case err := <-done:
		expectError(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a cancelled caller waited for its background attribution replay")
	}
	expectEqual(t, lines.all(), threeLines)
	heard.awaitOne(t)
	if deaths := heard.list(); len(deaths) != 1 || !errors.Is(deaths[0].cause, ReplayDropped) {
		t.Fatalf("the independent replay must still be heard once: %+v", deaths)
	}
	expectEqual(t, lines.all(), threeLines)
}

func awaitConsoleLines(t *testing.T, lines *_Heard, count int) {
	t.Helper()
	var deadline = time.Now().Add(10 * time.Second)
	for len(lines.all()) < count {
		if time.Now().After(deadline) {
			t.Fatal("the expected console lines never arrived")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTheRetentionBoundIncludesRebuildingTheReplay(t *testing.T) {
	heard, deathsOption := listenForDeaths()
	var sb = mustIsolated(t, deathsOption, WithBuildTimeout(5*time.Second))
	boundTheReplaysOf(t, sb, 200*time.Millisecond)
	var spin = mustExpression(t, sb, "(() => { while (true) {} })()")
	var log = recordRequests(t, sb)
	var d = dispatcherOf(t, sb)
	var replayed = make(chan *_Life, 1)
	d.mu.Lock()
	var start = d.start
	var once sync.Once
	d.start = func() (*_Life, error) {
		child, err := start()
		if err == nil {
			once.Do(func() {
				if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
					t.Errorf("stopping the replay child: %v", err)
				}
				replayed <- child
			})
		}
		return child, err
	}
	d.mu.Unlock()
	asking, cancel := context.WithCancel(t.Context())
	defer cancel()
	var done = make(chan error, 1)
	go func() { _, err := Resolve[any](asking, spin); done <- err }()
	awaitServed(t, log, spin, 1)
	killChild(t, sb)
	var child = <-replayed
	cancel()
	expectError(t, <-done, context.Canceled)
	select {
	case <-child.ended:
	case <-time.After(time.Second):
		t.Fatal("the abandoned replay ignored its retention bound while rebuilding")
	}
	heard.awaitOne(t)
	if deaths := heard.list(); len(deaths) != 1 || !errors.Is(deaths[0].cause, ReplayDropped) {
		t.Fatalf("retiring the overlong rebuild must report only ReplayDropped: %+v", deaths)
	}
	expectEqual(t, mustResolve[int](t, within(t, 5*time.Second), mustExpression(t, sb, "7")), 7)
}
