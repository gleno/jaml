package jaml

import (
	"context"
	"sync"
	"testing"
	"time"
)

func mustSecure(t *testing.T, text string, options ...Option) *SecureTemplate {
	t.Helper()
	template, err := MakeSecureTemplate(context.Background(), text, options...)
	if err != nil {
		t.Fatalf("MakeSecureTemplate: %v", err)
	}
	t.Cleanup(func() { _ = template.Close() })
	return template
}

func secureEvalAs[T any](t *testing.T, template *SecureTemplate, vars map[string]any, path ...string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out T
	if err := template.Eval(ctx, &out, vars, path...); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return out
}

func within(t *testing.T, limit time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	t.Cleanup(cancel)
	return ctx
}

func evaluatorPID(t *SecureTemplate) int {
	t.state.Lock()
	defer t.state.Unlock()
	if t.evaluator == nil {
		return 0
	}
	return t.evaluator.cmd.Process.Pid
}

func TestSecureAndTrustedAgree(t *testing.T) {
	text := `
config:
  value1: 123
  value2: ${1+1}
  value3: "some string ${{name}}"
  nested: "${ ({a: undefined, b: [undefined, 1.5, 'x', true, null], c: {d: -0}}) }"
  text: "n=${{ 1e21 }} ${{ [1, {z: 2, a: 1}] }}"
`
	vars := map[string]any{"name": "abc"}
	trusted := evalAs[map[string]any](t, mustTemplate(t, text), vars, "config")
	secure := secureEvalAs[map[string]any](t, mustSecure(t, text), vars, "config")
	expectEqual(t, secure, trusted)
}

func TestSecureNullAndUndefinedFollowTheSameRules(t *testing.T) {
	template := mustSecure(t, "a: ${undefined}\nb: ${null}\n", WithNullCoercion(Strict))
	var out map[string]any
	expectError(t, template.Eval(within(t, 10*time.Second), &out, nil), CoercionFailed, "$.a", "strict undefined")
	loose := mustSecure(t, "a: ${undefined}\nb: \"[${{null}}]\"\n", WithUndefinedCoercion(Loose))
	expectEqual(t, secureEvalAs[map[string]any](t, loose, nil), map[string]any{"a": nil, "b": "[]"})
}

func TestAnInfiniteLoopEndsWithItsContext(t *testing.T) {
	template := mustSecure(t, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 + 1 }\n")
	started := time.Now()
	var out any
	expectError(t, template.Eval(within(t, 100*time.Millisecond), &out, nil, "spin"), EvaluationFailed, "$.spin", "cancelled", "deadline exceeded")
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the context did not stop the loop in time: %s", elapsed)
	}
	expectEqual(t, secureEvalAs[int](t, template, nil, "ok"), 2)
}

func TestMemoryBombsHitTheLimit(t *testing.T) {
	bombs := map[string]string{
		"one huge string":   "v: \"${ 'x'.repeat(1e9).length }\"\n",
		"doubling":          "v: \"${ (() => { let s = 'x'; while (true) s += s })() }\"\n",
		"growing array":     "v: \"${ (() => { const a = []; while (true) a.push({k: a.length}) })() }\"\n",
		"huge filled array": "v: \"${ new Array(1e8).fill(0).length }\"\n",
	}
	for name, text := range bombs {
		t.Run(name, func(t *testing.T) {
			template := mustSecure(t, text+"ok: 1\n", WithMemoryLimit(64<<20))
			var out any
			expectError(t, template.Eval(within(t, 20*time.Second), &out, nil, "v"), EvaluationFailed, "$.v", "exceeded the memory limit of 67108864 bytes")
			expectEqual(t, secureEvalAs[int](t, template, nil, "ok"), 1)
		})
	}
}

func TestThrownErrorsKeepTheEvaluator(t *testing.T) {
	template := mustSecure(t, "bad: ${ (() => { throw new RangeError('too far') })() }\ngood: ${ 3 }\n")
	before := evaluatorPID(template)
	var out any
	expectError(t, template.Eval(within(t, 10*time.Second), &out, nil, "bad"), EvaluationFailed, "$.bad", "RangeError: too far")
	expectEqual(t, secureEvalAs[int](t, template, nil, "good"), 3)
	expectEqual(t, evaluatorPID(template), before)
}

func TestADeadEvaluatorIsReplaced(t *testing.T) {
	template := mustSecure(t, "spin: ${ (() => { while (true) {} })() }\nok: ${ 4 }\n")
	first := evaluatorPID(template)
	go func() {
		time.Sleep(100 * time.Millisecond)
		template.state.Lock()
		evaluator := template.evaluator
		template.state.Unlock()
		_ = evaluator.cmd.Process.Kill()
	}()
	var out any
	expectError(t, template.Eval(within(t, 10*time.Second), &out, nil, "spin"), EvaluationFailed, "$.spin", "evaluator died")
	expectEqual(t, secureEvalAs[int](t, template, nil, "ok"), 4)
	if replaced := evaluatorPID(template); replaced == first || replaced == 0 {
		t.Fatalf("expected a fresh evaluator, still %d", replaced)
	}
}

func TestCloseStopsTheEvaluator(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n")
	template.state.Lock()
	evaluator := template.evaluator
	template.state.Unlock()
	if err := template.Close(); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, evaluator.hasExited(), true)
	var out int
	expectError(t, template.Eval(within(t, time.Second), &out, nil, "v"), Closed)
	if err := template.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseInterruptsARunningEval(t *testing.T) {
	template := mustSecure(t, "spin: ${ (() => { while (true) {} })() }\n")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = template.Close()
	}()
	var out any
	expectError(t, template.Eval(context.Background(), &out, nil, "spin"), Closed)
}

func TestTheTemplateContextOwnsTheEvaluator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	template, err := MakeSecureTemplate(ctx, "v: ${ 1 }\n")
	if err != nil {
		t.Fatal(err)
	}
	template.state.Lock()
	evaluator := template.evaluator
	template.state.Unlock()
	cancel()
	select {
	case <-evaluator.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the evaluator outlived the template's context")
	}
	var out int
	expectError(t, template.Eval(within(t, time.Second), &out, nil, "v"), Closed)
}

func TestConcurrentSecureEvalsTakeTurns(t *testing.T) {
	template := mustSecure(t, "v: ${ n * 2 }\n")
	var wait sync.WaitGroup
	for n := range 16 {
		wait.Go(func() {
			var out int
			if err := template.Eval(within(t, 10*time.Second), &out, map[string]any{"n": n}, "v"); err != nil || out != n*2 {
				t.Errorf("n=%d: got %d, %v", n, out, err)
			}
		})
	}
	wait.Wait()
}

func TestTheEvaluatorGetsACleanEnvironment(t *testing.T) {
	t.Setenv("JAML_TEST_SECRET", "hunter2")
	template := mustSecure(t, "v: ${ 1 }\n")
	template.state.Lock()
	env := template.evaluator.cmd.Env
	template.state.Unlock()
	expectEqual(t, env, []string{evaluatorMarker + "=1", evaluatorMemoryLimit + "=268435456"})
}

func TestSecureTemplateErrorsSurfaceEarly(t *testing.T) {
	_, err := MakeSecureTemplate(context.Background(), "v: ${ 1 + }\n")
	expectError(t, err, InvalidTemplate, "$.v")
	_, err = MakeSecureTemplate(context.Background(), "v: 1\n", WithMemoryLimit(-1))
	expectError(t, err, InvalidTemplate, "memory limit")
	_, err = MakeTrustedTemplate("v: 1\n", WithMemoryLimit(1<<20))
	expectError(t, err, InvalidTemplate, "MakeSecureTemplate")
}

func TestSecureVarsMustBeJSONData(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n")
	var out int
	expectError(t, template.Eval(within(t, 10*time.Second), &out, map[string]any{"f": func() {}}, "v"), EvaluationFailed, `var "f" is not JSON data`)
	expectError(t, template.Eval(within(t, 10*time.Second), &out, map[string]any{"undefined": 1}, "v"), EvaluationFailed, `var "undefined" cannot be bound`)
}
