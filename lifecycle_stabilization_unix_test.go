//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func awaitLifecycleState(t *testing.T, label string, settled func() bool) {
	t.Helper()
	var deadline = time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var retry = time.NewTicker(time.Millisecond)
	defer retry.Stop()
	for !settled() {
		select {
		case <-retry.C:
		case <-deadline.C:
			t.Fatalf("lifecycle did not settle: %s", label)
		}
	}
}

func abandonLifecycleCopy(original *Expression) error {
	var owner = new(_ContextOwner)
	var origin = context.WithValue(context.Background(), _ContextOwnerKey{}, owner)
	var err error
	owner.expression, err = original.Copy(origin)
	return err
}

func lifecycleWorker(ctx context.Context, sb *Sandbox, value int) (*_Keep, error) {
	origin, endOrigin := context.WithCancel(ctx)
	defer endOrigin()
	original, err := sb.MakeExpression(origin, "value => value + 1")
	if err != nil {
		return nil, err
	}
	copiedWith, endCopy := context.WithCancel(ctx)
	defer endCopy()
	copied, err := original.Copy(copiedWith)
	if err != nil {
		return nil, err
	}
	if err := abandonLifecycleCopy(copied); err != nil {
		return nil, err
	}
	endOrigin()
	if _, err := Resolve[int](ctx, original, value); !errors.Is(err, Closed) {
		return nil, fmt.Errorf("ended original returned %v", err)
	}
	alreadyDone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Resolve[int](alreadyDone, copied, value); !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("canceled admission returned %v", err)
	}
	got, err := Resolve[int](ctx, copied, value)
	if err != nil || got != value+1 {
		return nil, fmt.Errorf("surviving copy returned %d, %v; want %d", got, err, value+1)
	}
	return copied.held.keep, nil
}

func TestConcurrentLifecycleSettlesAfterCrashReplayAndShutdown(t *testing.T) {
	if isolationSupported() != nil {
		t.Skip(isolationSupported())
	}
	lifetime, shutdown := context.WithCancel(t.Context())
	defer shutdown()
	var deaths = make(chan error, 8)
	sb, err := MakeSecureSandbox(lifetime, WithMaxConcurrency(4), WithAttributedDeaths(func(_ context.Context, death AttributedDeath) { deaths <- death.Cause }))
	if err != nil {
		t.Fatal(err)
	}
	var d = dispatcherOf(t, sb)
	var first = currentChild(t, sb)
	var childrenMu sync.Mutex
	var children = []*_Life{first}
	d.mu.Lock()
	var start = d.start
	d.start = func() (*_Life, error) {
		child, err := start()
		if child != nil {
			childrenMu.Lock()
			children = append(children, child)
			childrenMu.Unlock()
		}
		return child, err
	}
	d.replays = 200 * time.Millisecond
	d.mu.Unlock()
	source, endSource := context.WithCancel(t.Context())
	defer endSource()
	document, err := sb.MakeExpressionFromJaml(source, "good: '${7}'\nspin: '${(() => { console.log(\"ready\"); while (true) {} })()}'\n")
	if err != nil {
		t.Fatal(err)
	}
	stopSpin, spinning := keepRunning(t, document.At("spin"))
	defer stopSpin()
	type result struct {
		keep *_Keep
		err  error
	}
	var finished = make(chan result, 4)
	work, stopWork := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopWork()
	for value := range 4 {
		go func() {
			keep, err := lifecycleWorker(work, sb, value)
			finished <- result{keep: keep, err: err}
		}()
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var spinUnit = document.held.keep.unit.(*_RemoteUnit)
	awaitLifecycleState(t, "spinner admitted alone on a replacement", func() bool {
		d.mu.Lock()
		owned, alone := d.child.(_Exclusive)
		d.mu.Unlock()
		if !alone || owned.life == first {
			return false
		}
		owned.life.wire.mu.Lock()
		var rebuilt = owned.life.wire.residents[spinUnit] != nil
		owned.life.wire.mu.Unlock()
		owned.life.board.mu.Lock()
		defer owned.life.board.mu.Unlock()
		for _, pending := range owned.life.board.pending {
			if rebuilt && pending.op == opServe {
				return true
			}
		}
		return false
	})
	stopSpin()
	select {
	case err := <-spinning:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled spinner returned %v", err)
		}
	case <-work.Done():
		t.Fatal("canceled spinner did not return")
	}
	var keeps []*_Keep
	for range 4 {
		select {
		case result := <-finished:
			if result.err != nil {
				t.Fatal(result.err)
			}
			keeps = append(keeps, result.keep)
		case <-work.Done():
			t.Fatal("concurrent lifecycle worker did not return")
		}
	}
	select {
	case err := <-deaths:
		if !errors.Is(err, ReplayDropped) {
			t.Fatalf("abandoned replay was attributed as %v", err)
		}
	case <-work.Done():
		t.Fatal("abandoned replay did not finish its bound")
	}
	expectEqual(t, mustResolve[int](t, work, document.At("good")), 7)
	endSource()
	keeps = append(keeps, document.held.keep)
	for _, keep := range keeps {
		awaitHolderCount(t, keep, 0)
	}
	var current = currentChild(t, sb)
	awaitLifecycleState(t, "dispatcher and live wire empty", func() bool {
		d.mu.Lock()
		var idle = d.mode == sharing && d.running == 0 && len(d.waiting) == 0 && len(d.suspects) == 0
		d.mu.Unlock()
		current.wire.mu.Lock()
		idle = idle && len(current.wire.residents) == 0
		current.wire.mu.Unlock()
		current.board.mu.Lock()
		idle = idle && len(current.board.pending) == 0 && len(current.board.consoles) == 0
		current.board.mu.Unlock()
		return idle
	})
	shutdown()
	childrenMu.Lock()
	var observed = append([]*_Life(nil), children...)
	childrenMu.Unlock()
	for _, child := range observed {
		expectDeath(t, child)
		for _, done := range []<-chan struct{}{child.reaped, child.drained} {
			select {
			case <-done:
			default:
				t.Fatal("ended child still owns process or reply-reader work")
			}
		}
		for _, pipe := range []*os.File{child.wire.requests, child.wire.controls} {
			if _, err := pipe.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("ended child retained an open host pipe: %v", err)
			}
		}
		awaitLifecycleState(t, "dead child's residency and console cleanup", func() bool {
			child.wire.mu.Lock()
			var empty = len(child.wire.residents) == 0
			child.wire.mu.Unlock()
			child.board.mu.Lock()
			empty = empty && len(child.board.consoles) == 0
			child.board.mu.Unlock()
			return empty
		})
	}
	select {
	case err := <-deaths:
		t.Fatalf("one abandoned spinner produced an extra attributed death: %v", err)
	default:
	}
}
