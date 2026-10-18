package jaml

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type bothModes struct {
	trusted error
	secure  error
}

func makeInBothModes(t *testing.T, text string) bothModes {
	t.Helper()
	_, trusted := MakeTrustedTemplate(text)
	secure, secureErr := MakeSecureTemplate(context.Background(), text)
	if secureErr == nil {
		t.Cleanup(func() { _ = secure.Close() })
	}
	return bothModes{trusted: trusted, secure: secureErr}
}

func evalInBothModes(t *testing.T, text string, vars map[string]any) bothModes {
	t.Helper()
	var trustedOut, secureOut any
	trusted := mustTemplate(t, text).Eval(&trustedOut, vars)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secure := mustSecure(t, text).Eval(ctx, &secureOut, vars)
	if trusted == nil && secure == nil {
		expectEqual(t, secureOut, trustedOut)
	}
	return bothModes{trusted: trusted, secure: secure}
}

func (b bothModes) expectRefused(t *testing.T, sentinel error, fragments ...string) {
	t.Helper()
	expectError(t, b.trusted, sentinel, fragments...)
	expectError(t, b.secure, sentinel, fragments...)
	if b.trusted.Error() != b.secure.Error() {
		t.Fatalf("modes disagree:\ntrusted: %.300s\nsecure:  %.300s", b.trusted, b.secure)
	}
}

func (b bothModes) expectAccepted(t *testing.T) {
	t.Helper()
	if b.trusted != nil {
		t.Fatalf("trusted: %.300s", b.trusted)
	}
	if b.secure != nil {
		t.Fatalf("secure: %.300s", b.secure)
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

func TestResultsNestAtMost4096DeepInBothModes(t *testing.T) {
	evalInBothModes(t, "v: ${ "+nestedArrayExpression(4097)+" }\n", nil).expectRefused(t, EvaluationFailed, "$.v", "nests deeper than 4096")
	evalInBothModes(t, "v: ${ "+nestedArrayExpression(4096)+" }\n", nil).expectAccepted(t)
}

func TestInterpolatedResultsNestAtMost4096DeepInBothModes(t *testing.T) {
	evalInBothModes(t, "v: \"x${{ "+nestedArrayExpression(4097)+" }}\"\n", nil).expectRefused(t, EvaluationFailed, "nests deeper than 4096")
	evalInBothModes(t, "v: \"x${{ "+nestedArrayExpression(4096)+" }}\"\n", nil).expectAccepted(t)
}

func TestVarsNestAtMost4096DeepInBothModes(t *testing.T) {
	text := "v: ${ typeof deep }\n"
	evalInBothModes(t, text, map[string]any{"deep": nestedVar(4097)}).expectRefused(t, EvaluationFailed, `binding vars: var "deep" nests deeper than 4096`)
	evalInBothModes(t, text, map[string]any{"deep": nestedVar(4096)}).expectAccepted(t)
}

func TestTemplatesNestAtMost4096DeepInBothModes(t *testing.T) {
	makeInBothModes(t, deepTemplate(4097, "1")).expectRefused(t, InvalidTemplate, "nests deeper than 4096")
	makeInBothModes(t, deepTemplate(4096, "1")).expectAccepted(t)
}

func TestTemplatesNestedThroughAliasesCountTheirFullDepth(t *testing.T) {
	var text strings.Builder
	text.WriteString("a0: &a0 1\n")
	for link := 1; link <= 64; link++ {
		fmt.Fprintf(&text, "a%d: &a%d %s*a%d%s\n", link, link, strings.Repeat("[", 64), link-1, strings.Repeat("]", 64))
	}
	makeInBothModes(t, text.String()).expectRefused(t, InvalidTemplate, "$.a64", "nests deeper than 4096")
}

func TestTheDeepestTemplateHoldsTheDeepestResultInBothModes(t *testing.T) {
	evalInBothModes(t, deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`), nil).expectAccepted(t)
}

func TestBracketsInsideVarStringsAreNotNesting(t *testing.T) {
	quoted := strings.Repeat(`"[{`, 5000) + `\`
	evalInBothModes(t, "v: ${ deep[0].length }\n", map[string]any{"deep": []any{quoted, map[string]any{strings.Repeat("[", 5000): 1}}}).expectAccepted(t)
}
