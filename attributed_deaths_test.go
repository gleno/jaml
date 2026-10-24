//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"sync"
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
