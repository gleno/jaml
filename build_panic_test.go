package jaml

import "testing"

const panickingSource = `'\u{10FFFF}'`

func TestABuildThatPanicsIsAnInvalidTemplate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		before := servingChild(t, sb)
		_, err := sb.Compile(t.Context(), panickingSource)
		expectError(t, err, InvalidTemplate, "$: the engine failed")
		expectWellFormedAndBounded(t, err)
		_, err = sb.MakeTemplate(t.Context(), "a:\n  b: ${ "+panickingSource+" }\n")
		expectError(t, err, InvalidTemplate, "$.a.b: ")
		expectWellFormedAndBounded(t, err)
		_, err = sb.MakeTemplate(t.Context(), "a: x ${{ "+panickingSource+" }}\n")
		expectError(t, err, InvalidTemplate, "$.a: ")
		expectEqual(t, evalAs[int](t, mustTemplate(t, sb, "v: ${ 1 + 1 }\n"), nil, "v"), 2)
		expectEqual(t, servingChild(t, sb), before)
	})
}

func servingChild(t *testing.T, sb *Sandbox) int {
	t.Helper()
	if _, isIsolated := sb.engine.(*isolatedEngine); !isIsolated {
		return 0
	}
	return childPID(t, sb)
}
