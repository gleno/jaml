package jaml

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func failureOf(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected a *jaml.Failure in %v", err)
	}
	return failure
}

func expectFailure(t *testing.T, err error, path, reason string) {
	t.Helper()
	var failure = failureOf(t, err)
	if failure.Path != path || failure.Reason != reason {
		t.Fatalf("got Failure{Path: %q, Reason: %q}, want {%q, %q} from %q", failure.Path, failure.Reason, path, reason, err)
	}
}

func TestAFailureCarriesThePathAndTheReasonAlone(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a:\n  b: ${ (() => { throw new TypeError('no') })() }\n  c: ${ undefined }\n  d: \"x ${{ 1/0 }}\"\n")
		var out any

		var err = resolveInto(t.Context(), &out, document.At("a", "b"))
		expectFailure(t, err, "$.a.b", "TypeError: no")
		var thrown *Thrown
		if !errors.As(err, &thrown) || thrown.Message != failureOf(t, err).Reason {
			t.Fatalf("expected the thrown message as the reason, got %v and %v", thrown, err)
		}

		expectFailure(t, resolveInto(t.Context(), &out, document.At("a", "c")), "$.a.c", "result is undefined, which strict undefined coercion refuses")
		expectFailure(t, resolveInto(t.Context(), &out, document.At("a", "nothing")), "$.a", `has no key "nothing"`)
		expectFailure(t, resolveInto(t.Context(), &out, mustExpression(t, sb, "r => r / 0"), 1), "$", "result is Infinity"+notYAML)

		_, err = sb.MakeExpression(t.Context(), "1 +", AsFunction())
		expectFailure(t, err, "$", "syntax error at the end of the expression: Unexpected end of input")
		_, err = sb.MakeExpression(t.Context(), "parseInt", AsFunction())
		expectFailure(t, err, "$", errNotFunction.Error())
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a:\n  b: ${ 1 + }\n")
		expectFailure(t, err, "$.a.b", "syntax error at the end of the expression: Unexpected end of input")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a: ${ x ")
		if failure := failureOf(t, err); failure.Path != "$.a" || strings.HasPrefix(failure.Reason, "${") {
			t.Fatalf("expected the unterminated code left out of the reason, got %+v", failure)
		}
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a: [")
		if failure := failureOf(t, err); failure.Path != "$" || !strings.HasPrefix(failure.Reason, "yaml: ") {
			t.Fatalf("expected a YAML failure at the root, got %+v", failure)
		}

		expectFailure(t, fmt.Errorf("wrapped: %w", resolveInto(t.Context(), &out, document.At("a", "c"))), "$.a.c", "result is undefined, which strict undefined coercion refuses")
	})
}

func TestEveryKindOfFailureRebuildsItsMessage(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: ${ (() => { throw new Error('x') })() }\nb: ${ y }\nc: \"${{ undefined }}\"\nd: 1\n")
		var failures []error
		for _, path := range []string{"a", "b", "c", "missing"} {
			var out any
			failures = append(failures, resolveInto(t.Context(), &out, document.At(path)))
		}
		var number int
		failures = append(failures, resolveInto(t.Context(), &number, mustJaml(t, sb, "d: text\n").At("d")))
		_, err := sb.MakeExpression(t.Context(), "1", WithSigil("@"))
		failures = append(failures, err)
		for _, err := range failures {
			var failure = failureOf(t, err)
			if !strings.HasPrefix(err.Error(), failure.Path) || !strings.Contains(err.Error(), failure.Reason) || strings.Contains(failure.Reason, "expression error") {
				t.Fatalf("Failure %+v does not fit %q", failure, err)
			}
		}
	})
}
