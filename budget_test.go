package jaml

import (
	"reflect"
	"testing"
	"time"
)

func TestAnInfiniteLoopRunsOutOfTime(t *testing.T) {
	template := mustTemplate(t, "v: ${ (() => { while (true) {} })() }\n", WithTimeout(50*time.Millisecond))
	started := time.Now()
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "$.v (line 1, column 4)", "ran past its time budget of 50ms")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the budget did not stop the loop in time: %s", elapsed)
	}
}

func TestTheBudgetCoversTheWholeEval(t *testing.T) {
	template := mustTemplate(t, `
a: ${ (() => { const end = Date.now() + 40; while (Date.now() < end) {} return 1 })() }
b: ${ (() => { const end = Date.now() + 40; while (Date.now() < end) {} return 2 })() }
c: ${ (() => { const end = Date.now() + 40; while (Date.now() < end) {} return 3 })() }
`, WithTimeout(100*time.Millisecond))
	var out map[string]int
	expectError(t, template.Eval(&out, nil), EvaluationFailed, "ran past its time budget")
}

func TestEachEvalGetsAFreshBudget(t *testing.T) {
	template := mustTemplate(t, "v: ${ (() => { const end = Date.now() + 30; while (Date.now() < end) {} return 1 })() }\n", WithTimeout(200*time.Millisecond))
	for range 3 {
		expectEqual(t, evalAs[int](t, template, nil, "v"), 1)
	}
}

func TestAnExpiredBudgetFailsTheExpressionsAfterIt(t *testing.T) {
	template := mustTemplate(t, "a: ${ (() => { while (true) {} })() }\n", WithTimeout(20*time.Millisecond))
	var out any
	expectError(t, template.Eval(&out, nil), EvaluationFailed, "time budget")
}

func TestAGetterLoopRunsOutOfTime(t *testing.T) {
	template := mustTemplate(t, "v: \"${ ({ get g() { while (true) {} } }) }\"\n", WithTimeout(50*time.Millisecond))
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "time budget")
}

func TestDeepRecursionIsStopped(t *testing.T) {
	template := mustTemplate(t, "v: ${ (function f(n) { return f(n + 1) + 1 })(0) }\n")
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "maximum call depth")
}

func TestReasonableRecursionWorks(t *testing.T) {
	template := mustTemplate(t, "v: \"${ (function f(n) { return n === 0 ? 0 : f(n - 1) + 1 })(1000) }\"\n")
	expectEqual(t, evalAs[int](t, template, nil, "v"), 1000)
}

func TestTemplatesWithoutExpressionsNeverStartARuntime(t *testing.T) {
	template := mustTemplate(t, "a: 1\nb: [x, y]\n")
	evaluation := openEvaluation(template.settings, map[string]any{"unmarshalable": func() {}})
	defer evaluation.close()
	var out map[string]any
	if err := evaluation.decode(template.root, destination{target: reflect.ValueOf(&out).Elem(), at: rootLocation(template.rootAt)}); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, evaluation.session, (*session)(nil))
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
