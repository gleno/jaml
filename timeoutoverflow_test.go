package jaml

import (
	"context"
	"math"
	"testing"
	"testing/synctest"
	"time"
)

func TestWatchdogAllowancesDoNotShortenLargeTimeouts(t *testing.T) {
	for name, makeWatch := range map[string]func(context.Context) *_Oversight{
		"wall": func(ctx context.Context) *_Oversight {
			return _WallLimit(math.MaxInt64).overseen(ctx, cancelGrace)
		},
		"build": func(ctx context.Context) *_Oversight {
			return (&_RequestBody{limit: _WallLimit(time.Hour), compiles: true}).overseen(ctx, math.MaxInt64)
		},
		"rearmed": func(ctx context.Context) *_Oversight {
			watch := _WallLimit(math.MaxInt64-cancelGrace).overseen(ctx, 0)
			watch.rearm(2 * cancelGrace)
			return watch
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				watch := makeWatch(t.Context())
				defer watch.stop()
				time.Sleep(time.Second)
				if err := watch.ctx.Err(); err != nil {
					t.Fatalf("a positive long timeout expired after one second: %v", context.Cause(watch.ctx))
				}
			})
		})
	}
}

func TestWatchdogStillAppliesAllowanceAndCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		watch := _WallLimit(time.Second).overseen(t.Context(), time.Second)
		defer watch.stop()
		time.Sleep(time.Second)
		if watch.ctx.Err() != nil {
			t.Fatal("the watchdog discarded its allowance")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if _, ok := context.Cause(watch.ctx).(_WallExhausted); !ok {
			t.Fatalf("the finite watchdog did not expire: %v", context.Cause(watch.ctx))
		}
		ctx, cancel := context.WithCancel(t.Context())
		large := _WallLimit(math.MaxInt64).overseen(ctx, time.Second)
		defer large.stop()
		cancel()
		if large.ctx.Err() != context.Canceled {
			t.Fatal("a saturated watchdog lost caller cancellation")
		}
	})
}
