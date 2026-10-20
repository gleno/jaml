package jaml

import (
	"context"
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

func (l _WallLimit) overseen(ctx context.Context, allowance time.Duration) (context.Context, context.CancelFunc) {
	if l == 0 {
		return ctx, func() {}
	}
	return context.WithTimeoutCause(ctx, time.Duration(l)+allowance, l.exhausted())
}

func (l _WallLimit) exhausted() error {
	return _WallExhausted{limit: time.Duration(l)}
}
