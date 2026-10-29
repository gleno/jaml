package jaml

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
)

func TestSelectedFailurePathsFollowDocumentContainers(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, test := range []struct {
			name, source, want, script string
			path                       []string
			kind                       error
			resolve                    func(context.Context, *Expression) error
			options                    []Option
		}{
			{name: "mapping evaluation", source: `{"0":"${missing}"}`, path: []string{"0"}, want: `$["0"]`, kind: EvaluationFailed},
			{name: "list evaluation", source: `["${missing}"]`, path: []string{"0"}, want: `$[0]`, kind: EvaluationFailed},
			{name: "mapping missing child", source: `{"0":1}`, path: []string{"0", "child"}, want: `$["0"]`, kind: MissingPath},
			{name: "list missing child", source: `[1]`, path: []string{"0", "child"}, want: `$[0]`, kind: MissingPath},
			{name: "mixed evaluation", source: `{"0":[{"0":"${missing}"}]}`, path: []string{"0", "0", "0"}, want: `$["0"][0]["0"]`, kind: EvaluationFailed},
			{name: "mapping undefined", source: `{"0":"${undefined}"}`, path: []string{"0"}, want: `$["0"]`, kind: CoercionFailed},
			{name: "mapping null", source: `{"0":"${null}"}`, path: []string{"0"}, want: `$["0"]`, kind: CoercionFailed, options: []Option{WithNullCoercion(Strict)}},
			{name: "native scalar", source: `{"0":"bad"}`, path: []string{"0"}, want: `$["0"]`, kind: CoercionFailed, resolve: resolving[int]},
			{name: "native suffix", source: `{"0":[{"0":"bad"}]}`, path: []string{"0"}, want: `$["0"][0]["0"]`, kind: CoercionFailed, resolve: resolving[[]map[string]int]},
			{name: "script compile already placed", source: `{"0":1}`, path: []string{"0"}, want: `$`, kind: InvalidExpression, script: "let ="},
			{name: "script runtime", source: `{"0":1}`, path: []string{"0"}, want: `$["0"]`, kind: EvaluationFailed, script: "throw new Error('script failure')"},
		} {
			t.Run(test.name, func(t *testing.T) {
				ctx := t.Context()
				if test.script != "" {
					ctx = WithScript(ctx, test.script)
				}
				document := mustJaml(t, sb, test.source, test.options...)
				resolve := test.resolve
				if resolve == nil {
					resolve = resolving[any]
				}
				err := resolve(ctx, document.At(test.path...))
				expectError(t, err, test.kind)
				expectEqual(t, failureOf(t, err).Path, test.want)
				if test.script != "" {
					expectError(t, err, test.kind, "script 1:")
				}
				if test.kind == EvaluationFailed && test.script == "" || test.kind == CoercionFailed && test.resolve == nil {
					whole := resolve(ctx, document)
					expectEqual(t, failureOf(t, err).Path, failureOf(t, whole).Path)
				}
			})
		}
	})
}

func TestSelectedCoercionKeepsNativeErrorAndPartialValue(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var want []map[string]int
		original := json.Unmarshal([]byte(`[{"ok":1,"0":"bad"}]`), &want)
		got, err := Resolve[[]map[string]int](t.Context(), mustJaml(t, sb, `{"0":[{"ok":1,"0":"bad"}]}`).At("0"))
		expectError(t, err, CoercionFailed, original.Error())
		expectEqual(t, got, want)
		expectEqual(t, failureOf(t, err).Path, `$["0"][0]["0"]`)
		var reference, semantic *json.SemanticError
		if !errors.As(original, &reference) || !errors.As(err, &semantic) {
			t.Fatalf("missing native semantic cause: %v; %v", original, err)
		}
		expectEqual(t, *semantic, *reference)
	})
}

func TestLazyAndCopiedFailurePathsKeepNumericMappingKeys(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got := mustResolve[map[string]*Expression](t, t.Context(), mustJaml(t, sb, `{"0":"${missing}"}`))
		copied, err := got["0"].Copy(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range []*Expression{got["0"], copied} {
			_, err := Resolve[any](t.Context(), view)
			expectError(t, err, EvaluationFailed)
			expectEqual(t, failureOf(t, err).Path, `$["0"]`)
		}
	})
}

func TestSelectionAdmissionKeepsSyntacticFallback(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `{"0":1}`)
		_, err := Resolve[int](cancelledContext(t), document.At("0"))
		expectEqual(t, failureOf(t, err).Path, "$[0]")
		_, err = document.At("0").Copy(cancelledContext(t))
		expectEqual(t, failureOf(t, err).Path, "$[0]")
		_, err = Resolve[int](t.Context(), document.At("0", "\xff"))
		expectEqual(t, failureOf(t, err).Path, "$[0]")
	})
}
