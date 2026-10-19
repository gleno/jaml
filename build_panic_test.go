package jaml

import "testing"

const panickingSource = `'\u{10FFFF}'`

func TestABuildThatPanicsIsAnInvalidExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		before := servingChild(t, sb)
		_, err := sb.MakeExpression(t.Context(), panickingSource)
		expectError(t, err, InvalidExpression, "$: the engine failed")
		expectWellFormedAndBounded(t, err)
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a:\n  b: ${ "+panickingSource+" }\n")
		expectError(t, err, InvalidExpression, "$.a.b: ")
		expectWellFormedAndBounded(t, err)
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a: x ${{ "+panickingSource+" }}\n")
		expectError(t, err, InvalidExpression, "$.a: ")
		expectEqual(t, mustResolve[int](t, t.Context(), mustJaml(t, sb, "v: ${ 1 + 1 }\n").At("v")), 2)
		expectEqual(t, servingChild(t, sb), before)
	})
}

func servingChild(t *testing.T, sb *Sandbox) int {
	t.Helper()
	if _, isIsolated := sb.engine.(*_IsolatedEngine); !isIsolated {
		return 0
	}
	return childPID(t, sb)
}
