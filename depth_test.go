package jaml

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type _BothKinds struct {
	trusted  error
	isolated error
}

func makeInBothKinds(t *testing.T, text string) _BothKinds {
	t.Helper()
	_, trusted := sharedSandbox(t, trustedKind).MakeExpressionFromJaml(t.Context(), text)
	_, isolated := sharedSandbox(t, unprotectedKind).MakeExpressionFromJaml(t.Context(), text)
	return _BothKinds{trusted: trusted, isolated: isolated}
}

func evalInBothKinds(t *testing.T, text string, vars map[string]any) _BothKinds {
	t.Helper()
	return _Agreement{text: text, vars: vars}.eval(t, intoAny)
}

func (b _BothKinds) expectRefused(t *testing.T, sentinel error, fragments ...string) {
	t.Helper()
	expectError(t, b.trusted, sentinel, fragments...)
	expectError(t, b.isolated, sentinel, fragments...)
	if b.trusted.Error() != b.isolated.Error() {
		t.Fatalf("kinds disagree:\ntrusted:  %.300s\nisolated: %.300s", b.trusted, b.isolated)
	}
	for _, kind := range everyKind {
		if errors.Is(b.trusted, kind) != errors.Is(b.isolated, kind) {
			t.Fatalf("kinds disagree on %v:\ntrusted:  %.300s\nisolated: %.300s", kind, b.trusted, b.isolated)
		}
	}
}

func (b _BothKinds) expectAccepted(t *testing.T) {
	t.Helper()
	if b.trusted != nil {
		t.Fatalf("trusted: %.300s", b.trusted)
	}
	if b.isolated != nil {
		t.Fatalf("isolated: %.300s", b.isolated)
	}
}

func nestedArrayExpression(depth int) string {
	return fmt.Sprintf("(() => { let a = []; for (let i = 1; i < %d; i++) a = [a]; return a })()", depth)
}

func nestedVar(depth int) any {
	var nested any = []any{}
	for range depth - 1 {
		nested = []any{nested}
	}
	return nested
}

func deepTemplate(depth int, leaf string) string {
	return "v: " + strings.Repeat("[", depth-1) + leaf + strings.Repeat("]", depth-1) + "\n"
}

func TestResultsNestAtMost4096DeepInBothKinds(t *testing.T) {
	evalInBothKinds(t, "v: ${ "+nestedArrayExpression(4097)+" }\n", nil).expectRefused(t, EvaluationFailed, "$.v", "nests deeper than 4096")
	evalInBothKinds(t, "v: ${ "+nestedArrayExpression(4096)+" }\n", nil).expectAccepted(t)
}

func TestInterpolatedResultsNestAtMost4096DeepInBothKinds(t *testing.T) {
	evalInBothKinds(t, "v: \"x${{ "+nestedArrayExpression(4097)+" }}\"\n", nil).expectRefused(t, EvaluationFailed, "nests deeper than 4096")
	evalInBothKinds(t, "v: \"x${{ "+nestedArrayExpression(4096)+" }}\"\n", nil).expectAccepted(t)
}

func TestVarsNestAtMost4096DeepInBothKinds(t *testing.T) {
	var text = "v: ${ typeof deep }\n"
	evalInBothKinds(t, text, map[string]any{"deep": nestedVar(4097)}).expectRefused(t, InvalidInput, `binding values: value "deep" nests deeper than 4096`)
	evalInBothKinds(t, text, map[string]any{"deep": nestedVar(4096)}).expectAccepted(t)
}

func TestTemplatesNestAtMost4096DeepInBothKinds(t *testing.T) {
	makeInBothKinds(t, deepTemplate(4097, "1")).expectRefused(t, InvalidExpression, "nests deeper than 4096")
	makeInBothKinds(t, deepTemplate(4096, "1")).expectAccepted(t)
}

func TestTemplatesNestedThroughAliasesCountTheirFullDepth(t *testing.T) {
	var text strings.Builder
	text.WriteString("a0: &a0 1\n")
	for link := 1; link <= 64; link++ {
		fmt.Fprintf(&text, "a%d: &a%d %s*a%d%s\n", link, link, strings.Repeat("[", 64), link-1, strings.Repeat("]", 64))
	}
	makeInBothKinds(t, text.String()).expectRefused(t, InvalidExpression, "$.a64", "nests deeper than 4096")
}

func TestTheDeepestTemplateHoldsTheDeepestResultInBothKinds(t *testing.T) {
	evalInBothKinds(t, deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`), nil).expectAccepted(t)
}

func TestBracketsInsideVarStringsAreNotNesting(t *testing.T) {
	var quoted = strings.Repeat(`"[{`, 5000) + `\`
	evalInBothKinds(t, "v: ${ deep[0].length }\n", map[string]any{"deep": []any{quoted, map[string]any{strings.Repeat("[", 5000): 1}}}).expectAccepted(t)
}
