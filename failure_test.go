package jaml

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
		expectFailure(t, resolveInto(t.Context(), &out, mustExpression(t, sb, "r => r / 0"), 1), "$", "result is Infinity"+notData)

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

func TestALabelPrintsBeforeTheReasonButStaysOutOfIt(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var one = mustExpression(t, sb, "1")
		var out any

		var err = resolveInto(WithScript(t.Context(), "throw new Error('boom')"), &out, one)
		expectFailure(t, err, "$", "Error: boom")
		expectError(t, err, EvaluationFailed, "$: script 1: Error: boom: evaluation failed")
		var thrown *Thrown
		if !errors.As(err, &thrown) || thrown.Message != failureOf(t, err).Reason {
			t.Fatalf("expected the thrown message as the reason, got %v and %v", thrown, err)
		}

		err = resolveInto(WithScript(t.Context(), "let ="), &out, one)
		expectError(t, err, InvalidExpression, "$: script 1: ")
		if strings.Contains(failureOf(t, err).Reason, "script 1") {
			t.Fatalf("expected the script label left out of the reason, got %+v", failureOf(t, err))
		}

		err = resolveInto(WithWallLimit(WithScript(t.Context(), "for (;;) {}"), 50*time.Millisecond), &out, one)
		expectError(t, err, WallLimit, "$: script 1: exceeded the wall time limit of 50ms")
		expectFailure(t, err, "$", "exceeded the wall time limit of 50ms")

		err = resolveInto(WithValues(t.Context(), map[string]any{"x": make(chan int)}), &out, mustExpression(t, sb, "x"))
		expectError(t, err, InvalidInput, "$: binding values: value \"x\"")
		if strings.Contains(failureOf(t, err).Reason, "binding values") {
			t.Fatalf("expected the binding label left out of the reason, got %+v", failureOf(t, err))
		}
	})
}

func TestAFailurePrintsWhatItsErrorPrints(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: ${ (() => { throw new Error('x') })() }\nb: 1\n")
		var out any
		_, made := sb.MakeExpression(t.Context(), "1 +")
		_, copied := document.Copy(cancelledContext(t))
		for _, err := range []error{
			resolveInto(t.Context(), &out, document.At("a")),
			resolveInto(t.Context(), &out, document.At("zz")),
			resolveInto(WithScript(t.Context(), "throw 1"), &out, document.At("b")),
			resolveInto(WithWallLimit(t.Context(), -1), &out, document.At("b")),
			made,
			copied,
		} {
			var failure = failureOf(t, err)
			if failure.Path == "" || failure.Error() != err.Error() {
				t.Fatalf("Failure %+v prints %q, its error prints %q", failure, failure.Error(), err)
			}
		}
	})
	expectEqual(t, (&Failure{Path: "$.a", Reason: "boom"}).Error(), "$.a: boom")
}

func TestATimeLimitWhileTheSessionOpensNamesTheSelection(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "quick: ${ x }\n")
		var out any
		var err = resolveInto(WithWallLimit(WithValues(t.Context(), map[string]any{"x": 1}), time.Nanosecond), &out, document.At("quick"))
		expectError(t, err, WallLimit)
		if failure := failureOf(t, err); failure.Path != "$.quick" {
			t.Fatalf("expected the selection, got %+v from %q", failure, err)
		}
		err = resolveInto(WithWallLimit(WithScript(t.Context(), "for (;;) {}"), 50*time.Millisecond), &out, document.At("quick"))
		expectError(t, err, WallLimit, "$.quick: script 1: exceeded the wall time limit of 50ms")
	})
}
