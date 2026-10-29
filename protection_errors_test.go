package jaml

import (
	"errors"
	"testing"
)

func TestUserTypeErrorsKeepTheirMessageAndValueBesideProtectedWrites(t *testing.T) {
	const makeError = `const e = new TypeError("Cannot redefine property: custom"); e.tag = "kept"; throw e`
	var cases = map[string]string{
		"source getter":        `Object.assign(user, {get name(){` + makeError + `}})`,
		"descriptor getter":    `Object.defineProperty(user, "name", {get value(){` + makeError + `}})`,
		"descriptor list":      `Object.defineProperties(user, {get name(){` + makeError + `}})`,
		"array coercion":       `items.fill(0, {valueOf(){` + makeError + `}})`,
		"array getter":         `Array.prototype.pop.call(Object.freeze({length: 1, get 0(){` + makeError + `}}))`,
		"sort comparison":      `items.sort(() => {` + makeError + `})`,
		"assignment RHS":       `(() => {let target; target = (() => {` + makeError + `})()})()`,
		"empty assignment RHS": `(() => {let target; target = (() => {const e = new TypeError(""); e.tag = "kept"; throw e})()})()`,
	}
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		for name, source := range cases {
			t.Run(name, func(t *testing.T) {
				ctx := withValues(t, map[string]any{"user": map[string]any{"name": "a"}, "items": []int{2, 1}})
				_, err := Resolve[any](ctx, mustExpression(t, sb, source))
				expectError(t, err, EvaluationFailed)
				var caught *Thrown
				if !errors.As(err, &caught) {
					t.Fatalf("expected the user's TypeError, got %v", err)
				}
				var message = "TypeError: Cannot redefine property: custom"
				if name == "empty assignment RHS" {
					message = "TypeError"
				}
				expectEqual(t, caught.Message, message)
				expectEqual(t, caught.Value, map[string]any{"tag": "kept"})
			})
		}
	})
}
