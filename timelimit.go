package jaml

import (
	"context"
	"time"
)

type _TimeLimitKey struct{}

type _TimeLimit time.Duration

func WithTimeLimit(ctx context.Context, limit time.Duration) context.Context {
	if earlier, limited := ctx.Value(_TimeLimitKey{}).(_TimeLimit); limited && time.Duration(earlier) <= limit {
		return ctx
	}
	return context.WithValue(ctx, _TimeLimitKey{}, _TimeLimit(limit))
}

func timeLimitOf(ctx context.Context) (_TimeLimit, error) {
	limit, limited := ctx.Value(_TimeLimitKey{}).(_TimeLimit)
	if limited && limit <= 0 {
		return 0, fail(InvalidExpression, "a time limit must be positive, got %s", time.Duration(limit))
	}
	return limit, nil
}

func (l _TimeLimit) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if l == 0 {
		return ctx, func() {}
	}
	return context.WithTimeoutCause(ctx, time.Duration(l), l.exhausted())
}

func (l _TimeLimit) exhausted() error {
	return _TimeExhausted{limit: time.Duration(l)}
}
