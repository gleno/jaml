package jaml

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustIsolated(t *testing.T, options ...SandboxOption) *Sandbox {
	t.Helper()
	return mustSandbox(t, isolatedKind, options...)
}

func dispatcherOf(t *testing.T, sb *Sandbox) *_Dispatcher {
	t.Helper()
	isolated, isIsolated := sb.engine.(*_IsolatedEngine)
	if !isIsolated {
		t.Fatalf("%T is not an isolated sandbox", sb.engine)
	}
	return isolated.dispatcher
}

func currentChild(t *testing.T, sb *Sandbox) *_Life {
	t.Helper()
	var d = dispatcherOf(t, sb)
	var deadline = time.Now().Add(10 * time.Second)
	for {
		d.mu.Lock()
		current, isReady := d.child.(_Ready)
		d.mu.Unlock()
		if isReady && current.life.alive() {
			return current.life
		}
		if time.Now().After(deadline) {
			t.Fatal("the sandbox holds no evaluator")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func childPID(t *testing.T, sb *Sandbox) int {
	t.Helper()
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	if current, isReady := d.child.(_Ready); isReady {
		return current.life.cmd.Process.Pid
	}
	return 0
}

func expectDeath(t *testing.T, process *_Life) {
	t.Helper()
	select {
	case <-process.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the evaluator outlived its sandbox")
	}
}

func isolatedEvalAs[T any](t *testing.T, document *Expression, vars map[string]any, path ...string) T {
	t.Helper()
	var out T
	if err := resolveInto(valued(within(t, 30*time.Second), vars), &out, document.At(path...)); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return out
}

func evalBounded(t *testing.T, document *Expression, ctx context.Context, path string) error {
	t.Helper()
	var result = make(chan error, 1)
	go func() {
		var out any
		result <- resolveInto(ctx, &out, document.At(path))
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Eval outlived its context")
		return nil
	}
}

func liveChildren(t *testing.T) int {
	t.Helper()
	listed, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return len(strings.Fields(string(listed)))
}

func expectChildrenBackTo(t *testing.T, before int) {
	t.Helper()
	var deadline = time.Now().Add(5 * time.Second)
	for liveChildren(t) > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d evaluators are still running", liveChildren(t)-before)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheKindsAgree(t *testing.T) {
	var text = `
config:
  value1: 123
  value2: ${1+1}
  value3: "some string ${{name}}"
  nested: "${ ({a: undefined, b: [undefined, 1.5, 'x', true, null], c: {d: -0}}) }"
  text: "n=${{ 1e21 }} ${{ [1, {z: 2, a: 1}] }}"
`
	_Agreement{text: text, vars: map[string]any{"name": "abc"}, path: []string{"config"}}.eval(t, intoAny).expectAccepted(t)
}

func TestAnInfiniteLoopEndsWithItsContext(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 + 1 }\n")
		var started = time.Now()
		var out any
		var err = resolveInto(within(t, 100*time.Millisecond), &out, document.At("spin"))
		expectError(t, err, EvaluationFailed, "$.spin: cancelled: context deadline exceeded")
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("the context did not stop the loop in time: %s", elapsed)
		}
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("ok")), 2)
	})
}

func TestMemoryBombsHitTheLimit(t *testing.T) {
	var bombs = map[string]string{
		"one huge string":    "v: \"${ 'x'.repeat(1e9).length }\"\n",
		"doubling":           "v: \"${ (() => { let s = 'x'; while (true) s += s })() }\"\n",
		"growing array":      "v: \"${ (() => { const a = []; while (true) a.push({k: a.length}) })() }\"\n",
		"huge filled array":  "v: \"${ new Array(1e8).fill(0).length }\"\n",
		"past 4 GiB at once": "v: \"${ 'x'.repeat(5e9).length }\"\n",
	}
	for name, text := range bombs {
		t.Run(name, func(t *testing.T) {
			var document = mustJaml(t, mustIsolated(t, WithMemoryLimit(64<<20)), text+"ok: 1\n")
			var out any
			expectError(t, resolveInto(within(t, 20*time.Second), &out, document.At("v")), MemoryLimit, "$.v", "exceeded the memory limit of 67108864 bytes")
			expectEqual(t, isolatedEvalAs[int](t, document, nil, "ok"), 1)
		})
	}
}

func TestThrownErrorsKeepTheEvaluator(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "bad: ${ (() => { throw new RangeError('too far') })() }\ngood: ${ 3 }\n")
	var before = childPID(t, sb)
	var out any
	expectError(t, resolveInto(within(t, 10*time.Second), &out, document.At("bad")), EvaluationFailed, "$.bad", "RangeError: too far")
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "good"), 3)
	expectEqual(t, childPID(t, sb), before)
}

func TestADeadEvaluatorIsReplaced(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "slow: \"${ (() => { let n = 0; for (let i = 0; i < loops; i++) n += i % 3; return 7 })() }\"\nok: ${ 4 }\n")
	var first = childPID(t, sb)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if running, err := os.FindProcess(first); err == nil {
			_ = running.Kill()
		}
	}()
	expectEqual(t, isolatedEvalAs[int](t, document, map[string]any{"loops": 3e7 / raceSlowdown}, "slow"), 7)
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "ok"), 4)
	if replaced := childPID(t, sb); replaced == first {
		t.Fatalf("expected a fresh evaluator, still %d", replaced)
	}
}

func TestOneEvaluatorServesManyTemplatesAndExpressionsConcurrently(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(8))
	var before = liveChildren(t)
	var pid = childPID(t, sb)
	var documents = make([]*Expression, 12)
	var expressions = make([]*Expression, 12)
	for index := range documents {
		documents[index] = mustJaml(t, sb, fmt.Sprintf("v: ${ n * %d }\n", index))
		expressions[index] = mustExpression(t, sb, fmt.Sprintf("x => x + %d", index))
	}
	var group sync.WaitGroup
	for index := range documents {
		group.Go(func() {
			for round := range 20 {
				var fromTemplate, fromCall int
				if err := resolveInto(valued(within(t, 30*time.Second), map[string]any{"n": round}), &fromTemplate, documents[index].At("v")); err != nil || fromTemplate != round*index {
					t.Errorf("template %d round %d: got %d, %v", index, round, fromTemplate, err)
				}
				if err := resolveInto(within(t, 30*time.Second), &fromCall, expressions[index], round); err != nil || fromCall != round+index {
					t.Errorf("expression %d round %d: got %d, %v", index, round, fromCall, err)
				}
			}
		})
	}
	group.Wait()
	expectEqual(t, childPID(t, sb), pid)
	expectEqual(t, liveChildren(t), before)
}

func TestTheEvaluatorGetsACleanEnvironment(t *testing.T) {
	t.Setenv("JAML_TEST_SECRET", "hunter2")
	var sb = mustIsolated(t)
	var names []string
	for _, entry := range currentChild(t, sb).cmd.Env {
		name, value, _ := strings.Cut(entry, "=")
		names = append(names, name)
		if name == evaluatorMemoryLimit {
			expectEqual(t, value, "268435456")
		}
	}
	var want = []string{evaluatorMarker, evaluatorMemoryLimit, evaluatorNonce, evaluatorParent}
	for _, entry := range zoneEnv() {
		name, _, _ := strings.Cut(entry, "=")
		want = append(want, name)
	}
	expectEqual(t, names, want)
}

func TestBadOptionsSurfaceEarly(t *testing.T) {
	_, err := mustIsolated(t).MakeExpressionFromJaml(t.Context(), "v: ${ 1 + }\n")
	expectError(t, err, InvalidExpression, "$.v")
	_, err = MakeSecureSandbox(t.Context(), WithMemoryLimit(-1))
	expectError(t, err, InvalidExpression, "memory limit")
	_, err = MakeTrustedSandbox(t.Context(), WithMemoryLimit(64<<20))
	expectError(t, err, InvalidExpression, "MakeSecureSandbox")
	for _, kind := range sandboxKinds {
		_, err = kind.make(t.Context(), WithMaxConcurrency(0))
		expectError(t, err, InvalidExpression, "max concurrency must be at least 1, got 0")
		_, err = kind.make(t.Context(), WithBuildTimeout(0))
		expectError(t, err, InvalidExpression, "build timeout must be positive")
		_, err = kind.make(t.Context(), nil)
		expectError(t, err, InvalidExpression, "option 1 is nil")
	}
}

func TestMemoryLimitsOutsideTheirRangeAreRefused(t *testing.T) {
	for _, bytes := range []int64{0, -1, 16<<20 - 1, 1<<40 + 1, 1 << 62} {
		_, err := MakeSecureSandbox(t.Context(), WithMemoryLimit(bytes))
		expectError(t, err, InvalidExpression, "memory limit", "16 MiB", "1 TiB", fmt.Sprintf("got %d", bytes))
		_, err = MakeTrustedSandbox(t.Context(), WithMemoryLimit(bytes))
		expectError(t, err, InvalidExpression, "memory limit")
	}
}

func TestMemoryLimitsAtTheirBoundsServe(t *testing.T) {
	for _, bytes := range []int64{16 << 20, 1 << 40} {
		var document = mustJaml(t, mustIsolated(t, WithMemoryLimit(bytes)), "v: ${ 1 }\n")
		for range 100 {
			expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 1)
		}
	}
}

func TestTheEvaluatorRefusesALimitOutsideTheRange(t *testing.T) {
	t.Setenv(evaluatorParent, "1")
	t.Setenv(evaluatorNonce, "nonce")
	for _, written := range []string{"0", "1048576", "1099511627777"} {
		t.Setenv(evaluatorMemoryLimit, written)
		if _, err := worldFromEnv(); err == nil {
			t.Fatalf("the evaluator accepted %s=%s", evaluatorMemoryLimit, written)
		}
	}
}

func TestAnAnswerTooDeepNeverReachesTheWire(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "deep: ${ (() => { let a = []; for (let i = 0; i < 9999; i++) a = [a]; return a })() }\nok: ${ 5 }\n")
	var before = childPID(t, sb)
	expectError(t, evalBounded(t, document, within(t, 2*time.Second), "deep"), EvaluationFailed, "$.deep", "nests deeper than 4096")
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "ok"), 5)
	expectEqual(t, childPID(t, sb), before)
}

func TestVarsTooDeepNeverReachTheWire(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	var before = childPID(t, sb)
	var out int
	var err = resolveInto(valued(within(t, 10*time.Second), map[string]any{"deep": nestedVar(10001)}), &out, document.At("v"))
	expectError(t, err, EvaluationFailed, `value "deep" nests deeper than 4096`)
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 1)
	expectEqual(t, childPID(t, sb), before)
}
