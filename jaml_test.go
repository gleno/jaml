package jaml

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type _SandboxKind struct {
	name     string
	make     func(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
	isolated bool
}

type _SandboxKinds []_SandboxKind

var (
	trustedKind     = _SandboxKind{name: "trusted", make: MakeTrustedSandbox}
	isolatedKind    = _SandboxKind{name: "isolated", make: MakeSecureSandbox, isolated: true}
	unprotectedKind = _SandboxKind{name: "unprotected", make: MakeSecureSandboxWithoutBuiltinsProtection, isolated: true}
	sandboxKinds    = _SandboxKinds{trustedKind, isolatedKind}
	allSandboxKinds = _SandboxKinds{trustedKind, isolatedKind, unprotectedKind}
	protectedKinds  = _SandboxKinds{isolatedKind}
	mutableKinds    = _SandboxKinds{trustedKind, unprotectedKind}
)

func eachKind(t *testing.T, body func(t *testing.T, sb *Sandbox), options ...SandboxOption) {
	t.Helper()
	sandboxKinds.each(t, body, options...)
}

func (kinds _SandboxKinds) each(t *testing.T, body func(t *testing.T, sb *Sandbox), options ...SandboxOption) {
	t.Helper()
	for _, kind := range kinds {
		t.Run(kind.name, func(t *testing.T) {
			body(t, mustSandbox(t, kind, options...))
		})
	}
}

func eachProtection(t *testing.T, body func(t *testing.T, sb *Sandbox, protected bool)) {
	t.Helper()
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) { body(t, sb, true) })
	mutableKinds.each(t, func(t *testing.T, sb *Sandbox) { body(t, sb, false) })
}

func mustSandbox(t *testing.T, kind _SandboxKind, options ...SandboxOption) *Sandbox {
	t.Helper()
	if kind.isolated && isolationSupported() != nil {
		t.Skip(isolationSupported())
	}
	sb, err := kind.make(t.Context(), options...)
	if err != nil {
		t.Fatalf("making a %s sandbox: %v", kind.name, err)
	}
	return sb
}

type _TestKind struct {
	test *testing.T
	kind string
}

var sharedSandboxes sync.Map

func sharedSandbox(t *testing.T, kind _SandboxKind) *Sandbox {
	t.Helper()
	var key = _TestKind{test: t, kind: kind.name}
	if shared, found := sharedSandboxes.Load(key); found {
		return shared.(*Sandbox)
	}
	var made = mustSandbox(t, kind)
	sharedSandboxes.Store(key, made)
	t.Cleanup(func() { sharedSandboxes.Delete(key) })
	return made
}

func mustJaml(t *testing.T, sb *Sandbox, jaml string, options ...Option) *Expression {
	t.Helper()
	made, err := sb.MakeExpressionFromJaml(t.Context(), jaml, options...)
	if err != nil {
		t.Fatalf("MakeExpressionFromJaml: %v", err)
	}
	return made
}

func mustExpression(t *testing.T, sb *Sandbox, expression string, options ...Option) *Expression {
	t.Helper()
	made, err := sb.MakeExpression(t.Context(), expression, options...)
	if err != nil {
		t.Fatalf("MakeExpression: %v", err)
	}
	return made
}

func withValues(t *testing.T, values map[string]any) context.Context {
	return WithValues(t.Context(), values)
}

func valued(ctx context.Context, values map[string]any) context.Context {
	if values == nil {
		return ctx
	}
	return WithValues(ctx, values)
}

func evalAs[T any](t *testing.T, e *Expression, values map[string]any, path ...string) T {
	t.Helper()
	return mustResolve[T](t, valued(t.Context(), values), e.At(path...))
}

func resolveInto[T any](ctx context.Context, target *T, e *Expression, args ...any) error {
	resolved, err := Resolve[T](ctx, e, args...)
	if err == nil {
		*target = resolved
	}
	return err
}

func mustResolve[T any](t *testing.T, ctx context.Context, e *Expression, args ...any) T {
	t.Helper()
	resolved, err := Resolve[T](ctx, e, args...)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved
}

func within(t *testing.T, limit time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	t.Cleanup(cancel)
	return ctx
}

func expectError(t *testing.T, err error, sentinel error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v, got nil", sentinel)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected %v, got %v", sentinel, err)
	}
	if !errors.Is(err, ExpressionError) {
		t.Fatalf("expected every error to be an ExpressionError, got %v", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) || !strings.HasPrefix(err.Error(), failure.Path) || !strings.Contains(err.Error(), failure.Reason) {
		t.Fatalf("expected a Failure that fits %q, got %+v", err, failure)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("expected %q in %q", fragment, err.Error())
		}
	}
}

func expectEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTheSpecifiedExample(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
config:
  value1: 123
  value2: ${1+1}
  value3: "some string ${{name}}"
`, WithNullCoercion(Strict), WithUndefinedCoercion(Loose))
		type _Config struct {
			Value1 int    `json:"value1"`
			Value2 int    `json:"value2"`
			Value3 string `json:"value3"`
		}
		var got = mustResolve[_Config](t, withValues(t, map[string]any{"name": "abc"}), document.At("config"))
		expectEqual(t, got, _Config{Value1: 123, Value2: 2, Value3: "some string abc"})
	})
}

func TestTheExampleIntoAMap(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "config:\n  value1: 123\n  value2: ${1+1}\n  value3: \"some string ${{name}}\"\n")
		var got = mustResolve[map[string]any](t, withValues(t, map[string]any{"name": "abc"}), document.At("config"))
		expectEqual(t, got, map[string]any{"value1": float64(123), "value2": float64(2), "value3": "some string abc"})
	})
}

func TestAnExpressionEvaluatesNothingUntilResolve(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: ${ (() => { throw new Error('boom') })() }\nb: 1\n")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("b")), 1)
		_, err := Resolve[map[string]any](t.Context(), document)
		expectError(t, err, EvaluationFailed, "$.a", "Error: boom")
	})
}

func TestAnExpressionIsReusableWithDifferentValues(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var greeting = mustJaml(t, sb, "greeting: hello ${{who}}\n").At("greeting")
		expectEqual(t, mustResolve[string](t, withValues(t, map[string]any{"who": "ada"}), greeting), "hello ada")
		expectEqual(t, mustResolve[string](t, withValues(t, map[string]any{"who": "bob"}), greeting), "hello bob")
	})
}

var dollar = delimitersFor(defaultSigil)
