package jaml

import (
	"context"
	"math"
	"time"
)

type _WallLimitKey struct{}

type _WallLimit time.Duration

func WithWallLimit(ctx context.Context, limit time.Duration) context.Context {
	if earlier, limited := ctx.Value(_WallLimitKey{}).(_WallLimit); limited && time.Duration(earlier) <= limit {
		return ctx
	}
	return context.WithValue(ctx, _WallLimitKey{}, _WallLimit(limit))
}

func wallLimitOf(ctx context.Context) (_WallLimit, error) {
	limit, limited := ctx.Value(_WallLimitKey{}).(_WallLimit)
	if limited && limit <= 0 {
		return 0, fail(invalidInput, "a wall time limit must be positive, got %s", time.Duration(limit)).at(rootPlace)
	}
	return limit, nil
}

func (l _WallLimit) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if l == 0 {
		return ctx, func() {}
	}
	return context.WithTimeoutCause(ctx, time.Duration(l), l.exhausted())
}

type _Oversight struct {
	ctx   context.Context
	limit _WallLimit
	timer *time.Timer
	stop  context.CancelFunc
}

func (l _WallLimit) overseen(ctx context.Context, allowances ...time.Duration) *_Oversight {
	if l == 0 {
		return &_Oversight{ctx: ctx, stop: func() {}}
	}
	watched, cancel := context.WithCancelCause(ctx)
	var timer = time.AfterFunc(extendedTimeout(time.Duration(l), allowances...), func() { cancel(l.exhausted()) })
	return &_Oversight{ctx: watched, limit: l, timer: timer, stop: func() { timer.Stop(); cancel(context.Canceled) }}
}

func (o *_Oversight) rearm(allowance time.Duration) {
	if o.timer != nil {
		o.timer.Reset(extendedTimeout(time.Duration(o.limit), allowance))
	}
}

// Admitted timeouts and internal allowances are nonnegative. Extending one
// must never wrap it into an already-expired timer.
func extendedTimeout(timeout time.Duration, allowances ...time.Duration) time.Duration {
	for _, allowance := range allowances {
		if timeout > time.Duration(math.MaxInt64)-allowance {
			return time.Duration(math.MaxInt64)
		}
		timeout += allowance
	}
	return timeout
}

func (l _WallLimit) exhausted() error {
	return _WallExhausted{limit: time.Duration(l)}
}
