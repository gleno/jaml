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

type sandboxKind struct {
	name string
	make func(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
}

var (
	trustedKind  = sandboxKind{name: "trusted", make: MakeTrustedSandbox}
	isolatedKind = sandboxKind{name: "isolated", make: MakeSandbox}
	sandboxKinds = []sandboxKind{trustedKind, isolatedKind}
)

func eachKind(t *testing.T, body func(t *testing.T, sb *Sandbox), options ...SandboxOption) {
	t.Helper()
	for _, kind := range sandboxKinds {
		t.Run(kind.name, func(t *testing.T) {
			body(t, mustSandbox(t, kind, options...))
		})
	}
}

func mustSandbox(t *testing.T, kind sandboxKind, options ...SandboxOption) *Sandbox {
	t.Helper()
	if kind.name == isolatedKind.name && isolationSupported() != nil {
		t.Skip(isolationSupported())
	}
	sb, err := kind.make(t.Context(), options...)
	if err != nil {
		t.Fatalf("making a %s sandbox: %v", kind.name, err)
	}
	return sb
}

type testKind struct {
	test *testing.T
	kind string
}

var sharedSandboxes sync.Map

func sharedSandbox(t *testing.T, kind sandboxKind) *Sandbox {
	t.Helper()
	key := testKind{test: t, kind: kind.name}
	if shared, found := sharedSandboxes.Load(key); found {
		return shared.(*Sandbox)
	}
	made := mustSandbox(t, kind)
	sharedSandboxes.Store(key, made)
	t.Cleanup(func() { sharedSandboxes.Delete(key) })
	return made
}

func mustTemplate(t *testing.T, sb *Sandbox, text string, options ...Option) *Template {
	t.Helper()
	template, err := sb.MakeTemplate(t.Context(), text, options...)
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	return template
}

func mustCompile(t *testing.T, sb *Sandbox, source string) *Expression {
	t.Helper()
	compiled, err := sb.Compile(t.Context(), source)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return compiled
}

func evalAs[T any](t *testing.T, template *Template, vars map[string]any, path ...string) T {
	t.Helper()
	var out T
	if err := template.Eval(t.Context(), &out, vars, path...); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return out
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
	if !errors.Is(err, TemplateError) {
		t.Fatalf("expected every error to be a TemplateError, got %v", err)
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
		template := mustTemplate(t, sb, `
config:
  value1: 123
  value2: ${1+1}
  value3: "some string ${{name}}"
`, WithNullCoercion(Strict), WithUndefinedCoercion(Loose))
		type config struct {
			Value1 int    `json:"value1"`
			Value2 int    `json:"value2"`
			Value3 string `json:"value3"`
		}
		got := evalAs[config](t, template, map[string]any{"name": "abc"}, "config")
		expectEqual(t, got, config{Value1: 123, Value2: 2, Value3: "some string abc"})
	})
}

func TestTheExampleIntoAMap(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "config:\n  value1: 123\n  value2: ${1+1}\n  value3: \"some string ${{name}}\"\n")
		got := evalAs[map[string]any](t, template, map[string]any{"name": "abc"}, "config")
		expectEqual(t, got, map[string]any{"value1": float64(123), "value2": float64(2), "value3": "some string abc"})
	})
}

func TestATemplateEvaluatesNothingUntilEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: ${ (() => { throw new Error('boom') })() }\nb: 1\n")
		expectEqual(t, evalAs[int](t, template, nil, "b"), 1)
		var out map[string]any
		expectError(t, template.Eval(t.Context(), &out, nil), EvaluationFailed, "$.a", "Error: boom")
	})
}

func TestATemplateIsReusableWithDifferentVars(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "greeting: hello ${{who}}\n")
		expectEqual(t, evalAs[string](t, template, map[string]any{"who": "ada"}, "greeting"), "hello ada")
		expectEqual(t, evalAs[string](t, template, map[string]any{"who": "bob"}, "greeting"), "hello bob")
	})
}
