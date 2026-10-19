package jaml

import "testing"

func TestDeepRecursionIsStopped(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: ${ (function f(n) { return f(n + 1) + 1 })(0) }\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("v")), EvaluationFailed, "maximum call depth")
	})
}

func TestReasonableRecursionWorks(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: \"${ (function f(n) { return n === 0 ? 0 : f(n - 1) + 1 })(1000) }\"\n")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("v")), 1000)
	})
}

func TestTemplatesWithoutExpressionsStillCheckVars(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: 1\nb: [x, z]\n")
		var out any
		expectError(t, resolveInto(valued(t.Context(), map[string]any{"unmarshalable": func() {}}), &out, document), EvaluationFailed, `value "unmarshalable" is not JSON data`)
	})
}

func TestVarsThatAreNotJSONDataFailBeforeAnyExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: ${1}\n")
		var out int
		expectError(t, resolveInto(valued(t.Context(), map[string]any{"f": func() {}}), &out, document.At("v")), EvaluationFailed, `binding values: value "f" is not JSON data`)
	})
}

func TestVarsCannotRedefineImmutableGlobals(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: ${1}\n")
		var out int
		expectError(t, resolveInto(valued(t.Context(), map[string]any{"undefined": 1}), &out, document.At("v")), EvaluationFailed, `binding values: value "undefined" collides with a JavaScript global`)
	})
}

func TestEvalsWithoutExpressionsBuildNoEngine(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		cases := map[string]struct {
			text string
			path []string
		}{
			"template without code":         {"a: 1\nb: [x, y]\nc: {d: e}\n", nil},
			"selected subtree without code": {"a: ${ 1 }\nc: {d: e}\n", []string{"c"}},
		}
		vars := map[string]any{"fine": map[string]any{"k": []any{1, "two"}}}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				document := mustJaml(t, sb, c.text)
				allocations := testing.AllocsPerRun(20, func() {
					var out any
					if err := resolveInto(valued(t.Context(), vars), &out, document.At(c.path...)); err != nil {
						t.Fatal(err)
					}
				})
				if allocations > 200 {
					t.Fatalf("an Eval without expressions allocated %v times, as if it built an engine", allocations)
				}
			})
		}
	})
}
