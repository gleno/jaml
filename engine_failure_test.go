package jaml

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

const (
	stackCappedChild    = "JAML_STACK_CAPPED_CHILD"
	childStackBytes     = 64 << 20
	childMemoryBytes    = 256 << 20
	depthAWalkOverflows = 250_000
)

func survivesInAStackCappedChild(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	survivesInAChildWithMaxStack(t, childStackBytes, body)
}

func survivesInAChildWithMaxStack(t *testing.T, maxStack int, body func(t *testing.T)) {
	t.Helper()
	if os.Getenv(stackCappedChild) == t.Name() {
		debug.SetMaxStack(maxStack)
		debug.SetMemoryLimit(childMemoryBytes)
		body(t)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run", "^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), stackCappedChild+"="+t.Name())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child did not survive (%v):\n%.2000s", err, output)
	}
}

func nestedJSONText(depth int) string {
	return strings.Repeat("[", depth) + strings.Repeat("]", depth)
}

func expressionTemplate(source string) string {
	return "v: '${ " + strings.ReplaceAll(source, "'", "''") + " }'\n"
}

func deeplyThrown(thrown string) string {
	return expressionTemplate("(() => { let a = []; for (let i = 0; i < " + strconv.Itoa(depthAWalkOverflows) + "; i++) a = [a]; throw " + thrown + " })()")
}

func TestJSONParseRefusesTextNestedDeeperThan4096InBothKinds(t *testing.T) {
	text := "v: ${ JSON.parse(payload).length }\n"
	evalInBothKinds(t, text, map[string]any{"payload": nestedJSONText(4097)}).expectRefused(t, EvaluationFailed, "$.v", "RangeError", "JSON text nests deeper than 4096")
	evalInBothKinds(t, text, map[string]any{"payload": nestedJSONText(4096)}).expectAccepted(t)
}

func TestJSONParseGuardsTheTextAnObjectConvertsTo(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		text := expressionTemplate("JSON.parse({ toString: () => '['.repeat(4097) + ']'.repeat(4097) }).length")
		var out any
		expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, text).At("v")), EvaluationFailed, "JSON text nests deeper than 4096")
	})
}

func TestJSONParseDepthRefusalIsCatchable(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		text := expressionTemplate("(() => { try { JSON.parse(payload); return 'parsed' } catch (e) { return e instanceof RangeError } })()")
		expectEqual(t, mustResolve[any](t, valued(t.Context(), map[string]any{"payload": nestedJSONText(4097)}), mustJaml(t, sb, text).At("v")), true)
	})
}

func TestJSONParseStillParsesWithAReviver(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		text := expressionTemplate(`JSON.parse('{"a": 1}', (k, v) => typeof v === 'number' ? v + 1 : v)`)
		expectEqual(t, evalAs[map[string]int](t, mustJaml(t, sb, text), nil, "v"), map[string]int{"a": 2})
	})
}

func TestAnUntrustedMillionDeepJSONVarFailsTheEvalInsteadOfTheHost(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		survivesInAStackCappedChild(t, func(t *testing.T) {
			var out any
			err := resolveInto(valued(t.Context(), map[string]any{"payload": nestedJSONText(1 << 20)}), &out, mustJaml(t, sb, "v: ${ JSON.parse(payload) }\n").At("v"))
			expectError(t, err, EvaluationFailed, "JSON text nests deeper than 4096")
		})
	})
}

func TestADeeplyNestedThrownArrayIsDescribedWithoutWalkingIt(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		survivesInAStackCappedChild(t, func(t *testing.T) {
			var out any
			expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, deeplyThrown("a")).At("v")), EvaluationFailed, "threw an array")
		})
	})
}

func TestADeeplyNestedThrownMessageIsNotWalked(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		survivesInAStackCappedChild(t, func(t *testing.T) {
			var out any
			expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, deeplyThrown("{ message: a }")).At("v")), EvaluationFailed, "threw an object")
			expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, deeplyThrown("{ name: a, message: 'unnamed' }")).At("v")), EvaluationFailed, ": unnamed")
		})
	})
}

func TestThrownValuesAreDescribedWithoutConvertingObjects(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		cases := map[string]struct {
			source   string
			expected string
		}{
			"array":          {"throw [1, 2]", "threw an array"},
			"error":          {"throw new TypeError('t')", "TypeError: t"},
			"numeric fields": {"throw { name: 7, message: 8 }", "7: 8"},
			"object message": {"throw { message: { toString() { return 'm' } } }", "threw an object"},
			"object name":    {"throw { name: { toString() { return 'n' } }, message: 'unnamed' }", "}: unnamed"},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				var out any
				err := resolveInto(t.Context(), &out, mustJaml(t, sb, expressionTemplate("(() => { "+c.source+" })()")).At("v"))
				expectError(t, err, EvaluationFailed, c.expected)
			})
		}
	})
}

func TestAGoPanicInsideABuiltInFailsTheEvalInBothKinds(t *testing.T) {
	for name, source := range map[string]string{
		"repeat":   "'-'.repeat(width)",
		"padStart": "'x'.padStart(width).length",
	} {
		t.Run(name, func(t *testing.T) {
			evalInBothKinds(t, expressionTemplate(source), map[string]any{"width": 1 << 52}).expectRefused(t, EvaluationFailed, "$.v", "the engine failed", "runtime error")
		})
	}
}

func TestAPanickedSessionRunsNothingMore(t *testing.T) {
	vars, err := admitValues(map[string]any{"width": 1 << 52})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openSession(t.Context(), sessionPlan{values: vars})
	if err != nil {
		t.Fatal(err)
	}
	panicking, err := compileExpression("'-'.repeat(width)", dollar.value)
	if err != nil {
		t.Fatal(err)
	}
	harmless, err := compileExpression("1", dollar.value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.evaluate(panicking, nil); err == nil || !strings.Contains(err.Error(), "the engine failed") {
		t.Fatalf("expected the engine to fail, got %v", err)
	}
	if _, err := opened.evaluate(harmless, nil); err == nil || !strings.Contains(err.Error(), "an earlier expression broke the engine") {
		t.Fatalf("expected the broken session to refuse, got %v", err)
	}
}

func TestTheGuardedJSONParseLooksLikeTheBuiltIn(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		text := expressionTemplate("[JSON.parse.name, JSON.parse.length, Object.getOwnPropertyDescriptor(JSON.parse, 'name').writable, Object.getOwnPropertyDescriptor(JSON.parse, 'length').configurable, Object.keys(JSON.parse).length]")
		expectEqual(t, evalAs[[]any](t, mustJaml(t, sb, text), nil, "v"), []any{"parse", float64(2), false, true, float64(0)})
	})
}

func TestASessionRefusesEveryEntryOnceItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	opened, err := openSession(ctx, sessionPlan{})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	if err := opened.enter(func(realm) error { return nil }); err != nil {
		t.Fatalf("a live session refused: %v", err)
	}
	cancel()
	ran := false
	err = opened.enter(func(realm) error {
		ran = true
		return nil
	})
	if ran {
		t.Fatal("a step ran after its context ended")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the context's cause, got %v", err)
	}
}
