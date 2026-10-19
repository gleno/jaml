package jaml

import "testing"

func TestDeepRecursionIsStopped(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "v: ${ (function f(n) { return f(n + 1) + 1 })(0) }\n")
		var out any
		expectError(t, template.Eval(t.Context(), &out, nil, "v"), EvaluationFailed, "maximum call depth")
	})
}

func TestReasonableRecursionWorks(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "v: \"${ (function f(n) { return n === 0 ? 0 : f(n - 1) + 1 })(1000) }\"\n")
		expectEqual(t, evalAs[int](t, template, nil, "v"), 1000)
	})
}

func TestTemplatesWithoutExpressionsStillCheckVars(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: 1\nb: [x, z]\n")
		var out any
		expectError(t, template.Eval(t.Context(), &out, map[string]any{"unmarshalable": func() {}}), EvaluationFailed, `var "unmarshalable" is not JSON data`)
	})
}

func TestVarsThatAreNotJSONDataFailBeforeAnyExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "v: ${1}\n")
		var out int
		expectError(t, template.Eval(t.Context(), &out, map[string]any{"f": func() {}}, "v"), EvaluationFailed, `binding vars: var "f" is not JSON data`)
	})
}

func TestVarsCannotRedefineImmutableGlobals(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "v: ${1}\n")
		var out int
		expectError(t, template.Eval(t.Context(), &out, map[string]any{"undefined": 1}, "v"), EvaluationFailed, `binding vars: var "undefined" collides with a JavaScript global`)
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
				template := mustTemplate(t, sb, c.text)
				allocations := testing.AllocsPerRun(20, func() {
					var out any
					if err := template.Eval(t.Context(), &out, vars, c.path...); err != nil {
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
