package jaml

import "testing"

func TestDeepRecursionIsStopped(t *testing.T) {
	template := mustTemplate(t, "v: ${ (function f(n) { return f(n + 1) + 1 })(0) }\n")
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "maximum call depth")
}

func TestReasonableRecursionWorks(t *testing.T) {
	template := mustTemplate(t, "v: \"${ (function f(n) { return n === 0 ? 0 : f(n - 1) + 1 })(1000) }\"\n")
	expectEqual(t, evalAs[int](t, template, nil, "v"), 1000)
}

func TestTemplatesWithoutExpressionsNeverBindVars(t *testing.T) {
	template := mustTemplate(t, "a: 1\nb: [x, z]\n")
	got := evalAs[map[string]any](t, template, map[string]any{"unmarshalable": func() {}})
	expectEqual(t, got, map[string]any{"a": float64(1), "b": []any{"x", "z"}})
}

func TestVarsThatAreNotJSONDataFailTheFirstExpression(t *testing.T) {
	template := mustTemplate(t, "v: ${1}\n")
	var out int
	expectError(t, template.Eval(&out, map[string]any{"f": func() {}}, "v"), EvaluationFailed, "$.v", `var "f" is not JSON data`)
}

func TestVarsCannotRedefineImmutableGlobals(t *testing.T) {
	template := mustTemplate(t, "v: ${1}\n")
	var out int
	expectError(t, template.Eval(&out, map[string]any{"undefined": 1}, "v"), EvaluationFailed, `var "undefined" cannot be bound`)
}
