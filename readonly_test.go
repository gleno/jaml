package jaml

import (
	"errors"
	"testing"
)

func TestEveryWriteToAFrozenValueSaysItIsReadOnly(t *testing.T) {
	var values = map[string]any{"items": []int{3, 1, 2}, "user": map[string]any{"name": "a", "count": 1}}
	var writes = []string{
		"items.sort()",
		"items.sort((a, b) => a - b)",
		"items.reverse()",
		"items.fill(0)",
		"items.copyWithin(0, 1)",
		"items.splice(0, 1)",
		"items.push(4)",
		"items.pop()",
		"items.shift()",
		"items.unshift(0)",
		"Object.defineProperty(user, 'name', { value: 'b' })",
		"Object.defineProperties(user, { name: { value: 'b' } })",
		"Object.assign(user, { name: 'b' })",
		"Object.setPrototypeOf(user, null)",
		"items[0] = 9",
		"user.name = 'b'",
		"user.extra = 1",
		"user.count += 1",
		"items[0]++",
		"--user.count",
		"delete user.name",
		"delete items[0]",
		"[items[0]] = [9]",
		"({ name: user.name } = { name: 'b' })",
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, write := range writes {
			var out any
			err := resolveInto(withValues(t, values), &out, mustExpression(t, sb, write))
			expectError(t, err, EvaluationFailed, "$: ", "TypeError: "+readOnlyRefusal)
			var thrown *Thrown
			if !errors.As(err, &thrown) || thrown.Message != "TypeError: "+readOnlyRefusal {
				t.Fatalf("%s: the thrown message is %v", write, thrown)
			}
		}
	})
}

func TestATypeErrorThatIsNotAWriteKeepsItsMessage(t *testing.T) {
	var values = map[string]any{"items": []int{3, 1, 2}, "user": map[string]any{"name": "a"}}
	var cases = map[string]string{
		"user.missing.deeper":                              "TypeError: Cannot read property 'deeper' of undefined",
		"items.sort(() => { throw new TypeError('cmp') })": "TypeError: cmp",
		"(() => { throw new TypeError('') })()":            "TypeError",
		"user.name.x = 1":                                  "TypeError: Receiver is not an object: a",
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for source, message := range cases {
			var out any
			err := resolveInto(withValues(t, values), &out, mustExpression(t, sb, source))
			var thrown *Thrown
			if !errors.As(err, &thrown) || thrown.Message != message {
				t.Fatalf("%s: want %q, got %v", source, message, err)
			}
		}
	})
}

func TestAFrozenWriteThatDoesNothingStillSucceeds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got := mustResolve[[]int](t, withValues(t, map[string]any{"none": []int{}, "one": []int{1}}), mustExpression(t, sb, "[...none.sort(), ...one.reverse()]"))
		expectEqual(t, got, []int{1})
	})
}

func TestAGuardedMethodKeepsItsNameAndLength(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got := mustResolve[[]any](t, t.Context(), mustExpression(t, sb, "[Array.prototype.splice.name, Array.prototype.splice.length, Object.defineProperty.name, Object.defineProperty.length]"))
		expectEqual(t, got, []any{"splice", float64(2), "defineProperty", float64(3)})
	})
}
