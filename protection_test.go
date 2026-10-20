package jaml

import (
	"errors"
	"testing"
)

const sharedWrites = "a: ${ (user.order.total = 0, items.sort(), items.push(4), 1) }\nb: ${ [user.order.total, items] }\n"

func protectionValues() map[string]any {
	return map[string]any{"user": map[string]any{"order": map[string]any{"total": 9}}, "items": []int{3, 1, 2}}
}

func TestUnprotectedValuesAreOrdinaryDataWithinOneResolve(t *testing.T) {
	mutableKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, sharedWrites)
		var ctx = WithValues(t.Context(), protectionValues())
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{"a": float64(1), "b": []any{float64(0), []any{float64(1), float64(2), float64(3), float64(4)}}})
		expectEqual(t, mustResolve[[]any](t, ctx, document.At("b")), []any{float64(9), []any{float64(3), float64(1), float64(2)}})
	})
}

func TestProtectedValuesStayFrozen(t *testing.T) {
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(WithValues(t.Context(), protectionValues()), &out, mustJaml(t, sb, sharedWrites))
		expectError(t, err, EvaluationFailed, "$.a: ", "TypeError: "+readOnlyRefusal)
	})
}

func TestTopLevelValueNamesStayReadOnlyEverywhere(t *testing.T) {
	var writes = []string{"user = 1", "globalThis.user = 1", "this.user = 1", "Reflect.set(globalThis, 'user', 1)", "Function('user = 1')()"}
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		for _, write := range writes {
			var out any
			var err = resolveInto(WithValues(t.Context(), protectionValues()), &out, mustExpression(t, sb, write))
			var thrown *Thrown
			if !errors.As(err, &thrown) || thrown.Message[:len("TypeError: cannot change a read-only value")] != "TypeError: cannot change a read-only value" {
				t.Fatalf("%s: want a read-only refusal, got %v", write, err)
			}
		}
	})
}
