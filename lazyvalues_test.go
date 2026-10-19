package jaml

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func manyObjects(count int) []map[string]any {
	var objects = make([]map[string]any, count)
	for i := range objects {
		objects[i] = map[string]any{"id": i, "name": fmt.Sprintf("item %d", i), "tags": []string{"a", "b"}}
	}
	return objects
}

func TestAnUnreadValueTakesNoMemoryInTheChild(t *testing.T) {
	sb, err := MakeSecureSandbox(t.Context(), WithMemoryLimit(smallestMemoryLimit))
	if err != nil {
		t.Fatal(err)
	}
	var values = map[string]any{"big": manyObjects(40_000), "small": 1}
	expectEqual(t, mustResolve[float64](t, withValues(t, values), mustExpression(t, sb, "small + 1")), float64(2))
	var _, read = Resolve[float64](withValues(t, values), mustExpression(t, sb, "big.length"))
	expectError(t, read, MemoryLimit)
}

func TestASessionPaysOnlyForTheValuesItReads(t *testing.T) {
	values, err := admitValues(map[string]any{"big": manyObjects(100_000), "small": 1})
	if err != nil {
		t.Fatal(err)
	}
	var session = func(source string) time.Duration {
		compiled, err := compileExpression(source, dollar.value)
		if err != nil {
			t.Fatal(err)
		}
		var started = time.Now()
		opened, err := openSession(t.Context(), _SessionPlan{values: values})
		if err != nil {
			t.Fatal(err)
		}
		defer opened.close()
		if _, err := opened.evaluate(compiled, nil); err != nil {
			t.Fatal(err)
		}
		return time.Since(started)
	}
	var unread, read = session("small + 1"), session("big.length")
	t.Logf("unread %v, read %v", unread, read)
	if unread*10 > read {
		t.Fatalf("a session that never read a value spent %v against %v reading it", unread, read)
	}
}

func TestAValueIsNotBoundUntilCodeReadsIt(t *testing.T) {
	var opened, err = openSession(t.Context(), _SessionPlan{values: _BoundValues{"broken": {encoded: json.RawMessage(`{`)}}})
	if err != nil {
		t.Fatalf("a session bound a value nobody read: %v", err)
	}
	defer opened.close()
	harmless, err := compileExpression("1", dollar.value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.evaluate(harmless, nil); err != nil {
		t.Fatalf("code that never reads a value failed: %v", err)
	}
	catching, err := compileExpression("(() => { try { return broken } catch { return 'caught' } })()", dollar.value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := opened.evaluate(catching, nil)
	if err == nil || !strings.Contains(err.Error(), `value "broken" cannot be bound: SyntaxError`) {
		t.Fatalf("a value that cannot bind gave %v, %v", result, err)
	}
	if _, err := opened.evaluate(harmless, nil); err == nil {
		t.Fatal("a session ran on after a value failed to bind")
	}
}

func TestAValueReadsTheSameWayEverywhere(t *testing.T) {
	var reads = "[typeof user, user === this.user, user === globalThis.user, 'user' in globalThis, Object.keys(globalThis).filter(k => ['alpha', 'user', 'zeta'].includes(k)), user.name, Object.isFrozen(user) && Object.isFrozen(user.tags), JSON.stringify({ ...globalThis }.user)]"
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var ctx = withValues(t, map[string]any{"zeta": 1, "user": map[string]any{"name": "a", "tags": []string{"x"}}, "alpha": 2})
		expectEqual(t, mustResolve[[]any](t, ctx, mustExpression(t, sb, reads)), []any{
			"object", true, true, true, []any{"alpha", "user", "zeta"}, "a", true, `{"name":"a","tags":["x"]}`,
		})
	})
}

func TestAValueReadInAScriptIsTheOneEveryLeafSees(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var ctx = WithValues(WithScript(t.Context(), "const seen = user; const frozen = Object.isFrozen(user.tags)"), map[string]any{"user": map[string]any{"tags": []string{"x"}}})
		var document = mustJaml(t, sb, "a: ${ seen === user && frozen }\nb: ${ seen === user }\n")
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{"a": true, "b": true})
	})
}

func TestAValueCannotBeDeletedOrRedefinedBeforeOrAfterItIsRead(t *testing.T) {
	var attempts = "[Reflect.deleteProperty(globalThis, 'user'), Reflect.defineProperty(globalThis, 'user', { value: 1 }), Reflect.defineProperty(globalThis, 'user', { get: () => 1 }), Reflect.defineProperty(globalThis, 'user', { enumerable: false })]"
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var values = map[string]any{"user": map[string]any{"name": "a"}}
		var document = mustJaml(t, sb, "before: \"${ "+attempts+" }\"\nread: ${ user.name }\nafter: \"${ "+attempts+" }\"\nstill: ${ user.name }\n")
		expectEqual(t, mustResolve[map[string]any](t, withValues(t, values), document), map[string]any{
			"before": []any{false, false, false, false},
			"read":   "a",
			"after":  []any{false, false, false, false},
			"still":  "a",
		})
		var scripted = WithValues(WithScript(t.Context(), "globalThis.attempted = "+attempts), values)
		expectEqual(t, mustResolve[[]any](t, scripted, mustExpression(t, sb, "[...attempted, user.name]")), []any{false, false, false, false, "a"})
		for _, write := range []string{"delete globalThis.user", "Object.defineProperty(globalThis, 'user', { value: 1 })"} {
			var _, err = Resolve[any](withValues(t, values), mustExpression(t, sb, write))
			expectError(t, err, EvaluationFailed, "TypeError")
		}
	})
}

func TestAValueDescriptorShowsItsAccessors(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var described = "(d => [typeof d.get, typeof d.set, d.enumerable, d.configurable, 'value' in d, Object.isFrozen(d.get), Object.isFrozen(d.set)])(Object.getOwnPropertyDescriptor(globalThis, 'user'))"
		expectEqual(t, mustResolve[[]any](t, withValues(t, map[string]any{"user": 1}), mustExpression(t, sb, described)), []any{"function", "function", true, false, false, true, true})
	})
}
