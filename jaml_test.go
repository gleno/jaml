package jaml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustTemplate(t *testing.T, text string, options ...Option) *Template {
	t.Helper()
	template, err := MakeTemplate(text, options...)
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	return template
}

func evalAs[T any](t *testing.T, template *Template, vars map[string]any, path ...string) T {
	t.Helper()
	var out T
	if err := template.Eval(&out, vars, path...); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return out
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
	template := mustTemplate(t, `
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
}

func TestTheExampleIntoAMap(t *testing.T) {
	template := mustTemplate(t, "config:\n  value1: 123\n  value2: ${1+1}\n  value3: \"some string ${{name}}\"\n")
	got := evalAs[map[string]any](t, template, map[string]any{"name": "abc"}, "config")
	expectEqual(t, got, map[string]any{"value1": float64(123), "value2": float64(2), "value3": "some string abc"})
}

func TestATemplateEvaluatesNothingUntilEval(t *testing.T) {
	template := mustTemplate(t, "a: ${ (() => { throw new Error('boom') })() }\nb: 1\n")
	expectEqual(t, evalAs[int](t, template, nil, "b"), 1)
	var out map[string]any
	expectError(t, template.Eval(&out, nil), EvaluationFailed, "$.a", "Error: boom")
}

func TestATemplateIsReusableWithDifferentVars(t *testing.T) {
	template := mustTemplate(t, "greeting: hello ${{who}}\n")
	expectEqual(t, evalAs[string](t, template, map[string]any{"who": "ada"}, "greeting"), "hello ada")
	expectEqual(t, evalAs[string](t, template, map[string]any{"who": "bob"}, "greeting"), "hello bob")
}
