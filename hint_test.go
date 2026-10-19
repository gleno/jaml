package jaml

import (
	"strings"
	"testing"
)

const objectLiteralHint = "an arrow returning an object literal needs parentheses: () => ({ ... })"

func TestAnArrowWithABracedBodyThatReturnsUndefinedIsHinted(t *testing.T) {
	type verdict struct {
		OK bool `json:"ok"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "leaf: \"${ (d) => { ok: true } }\"\nthunk: \"${ (d) => () => { ok: true } }\"\nnamed: \"${ function (d) { } }\"\nconcise: \"${ (d) => undefined }\"\ncalled: \"${ (() => { ok: true })() }\"\n")
		for _, path := range []string{"leaf", "thunk"} {
			_, err := Resolve[verdict](t.Context(), document.At(path), 1)
			expectError(t, err, CoercionFailed, "$."+path+": ", "is undefined, which strict undefined coercion refuses; "+objectLiteralHint)
		}
		_, err := Resolve[verdict](t.Context(), mustExpression(t, sb, "(d) => { ok: true }", AsFunction()), 1)
		expectError(t, err, CoercionFailed, "$: ", objectLiteralHint)
		for _, path := range []string{"named", "concise", "called"} {
			_, err := Resolve[verdict](t.Context(), document.At(path), 1)
			expectError(t, err, CoercionFailed, "is undefined, which strict undefined coercion refuses")
			if strings.Contains(err.Error(), objectLiteralHint) {
				t.Fatalf("%s: hinted %q", path, err)
			}
		}
	})
}
