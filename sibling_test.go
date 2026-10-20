package jaml

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestARunawayRegexDoesNotStallItsSibling(t *testing.T) {
	var sb = mustSandbox(t, unprotectedKind, WithMemoryLimit(128<<20), WithMaxConcurrency(2*runtime.GOMAXPROCS(0)))
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var innocent = mustExpression(t, sb, "1 + 1")
	var worst time.Duration
	for range 8 {
		var ran = make(chan error, 1)
		go func() {
			var out any
			ran <- resolveInto(WithWallLimit(t.Context(), 100*time.Millisecond), &out, runaway)
		}()
		time.Sleep(10 * time.Millisecond)
		var started = time.Now()
		expectEqual(t, mustResolve[int](t, t.Context(), innocent), 2)
		worst = max(worst, time.Since(started))
		expectError(t, <-ran, EvaluationFailed)
	}
	if worst > 100*time.Millisecond*raceSlowdown {
		t.Fatalf("an innocent evaluation waited %s beside a runaway regex", worst)
	}
}

func TestAJobLostToStuckDeathsReplaysSharedOnlyOnce(t *testing.T) {
	var d = dispatcherOf(t, mustIsolated(t))
	var modes []_Mode
	var outcome = d.carry(t.Context(), func(e *_Errand, l *_Life) _Outcome {
		d.mu.Lock()
		modes = append(modes, d.mode)
		d.mu.Unlock()
		if len(modes) < 3 {
			return _Lost{cause: _StuckDeath("stuck")}
		}
		return _Built{}
	})
	if _, built := outcome.(_Built); !built {
		t.Fatalf("a job lost twice to stuck deaths ended %#v", outcome)
	}
	expectEqual(t, modes, []_Mode{sharing, sharing, recovering})
}

func TestAnInnocentJobOutlastsAStreamOfRunaways(t *testing.T) {
	var sb = mustSandbox(t, unprotectedKind, WithMemoryLimit(128<<20), WithMaxConcurrency(2*runtime.GOMAXPROCS(0)))
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var innocent = mustExpression(t, sb, "x => { let n = 0; for (let i = 0; i < x; i++) n += i % 3; return n }")
	var loops = 6_000_000 / raceSlowdown
	var started = time.Now()
	expectEqual(t, mustResolve[int](t, t.Context(), innocent, loops), loops)
	t.Logf("alone it takes %s", time.Since(started))
	var done = make(chan struct{})
	var streaming sync.WaitGroup
	for range 2 {
		streaming.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				var out any
				_ = resolveInto(WithWallLimit(t.Context(), 100*time.Millisecond), &out, runaway)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	var killed = currentChild(t, sb)
	started = time.Now()
	expectEqual(t, mustResolve[int](t, within(t, 60*time.Second), innocent, loops), loops)
	t.Logf("beside the stream it took %s", time.Since(started))
	close(done)
	streaming.Wait()
	if killed.alive() {
		t.Fatal("no runaway killed the child the innocent job started on")
	}
}
