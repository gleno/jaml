package jaml

import (
	"context"
	"maps"
	"slices"
)

type valuesKey struct{}

type scriptsKey struct{}

func WithValues(ctx context.Context, values map[string]any) context.Context {
	var merged = maps.Clone(valuesOf(ctx))
	if merged == nil {
		merged = make(map[string]any, len(values))
	}
	maps.Copy(merged, values)
	return context.WithValue(ctx, valuesKey{}, merged)
}

func WithScript(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, scriptsKey{}, append(slices.Clip(scriptsOf(ctx)), source))
}

func valuesOf(ctx context.Context) map[string]any {
	var values, _ = ctx.Value(valuesKey{}).(map[string]any)
	return values
}

func scriptsOf(ctx context.Context) []string {
	var scripts, _ = ctx.Value(scriptsKey{}).([]string)
	return scripts
}
