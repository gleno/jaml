package jaml

import (
	"testing"
)

func TestASigilChangesWhatOpensCode(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
on: push
jobs:
  build:
    runs-on: ${{ matrix.os }}
    steps:
      - run: echo ${{ github.sha }} @{{ who }}
      - count: "@{ 1 + 1 }"
`, WithSigil("@"))
		type _Step struct {
			Run   string `json:"run"`
			Count int    `json:"count"`
		}
		type _Workflow struct {
			On   string `json:"on"`
			Jobs map[string]struct {
				RunsOn string  `json:"runs-on"`
				Steps  []_Step `json:"steps"`
			} `json:"jobs"`
		}
		got := mustResolve[_Workflow](t, withValues(t, map[string]any{"who": "ada"}), document)
		expectEqual(t, got.Jobs["build"].RunsOn, "${{ matrix.os }}")
		expectEqual(t, got.Jobs["build"].Steps, []_Step{{Run: "echo ${{ github.sha }} ada"}, {Count: 2}})
	})
}

func TestALongerSigilWorks(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: \"%%{ 40 + 2 }\"\nb: x %%{{ 'y' }} ${{ z }}\n", WithSigil("%%"))
		expectEqual(t, mustResolve[map[string]any](t, t.Context(), document), map[string]any{"a": float64(42), "b": "x y ${{ z }}"})
	})
}

func TestABadSigilIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for sigil, fragment := range map[string]string{"": "must not be empty", "{": "brace", "a}": "brace", " ": "whitespace", "@\t": "whitespace", "\xff": "valid UTF-8"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n", WithSigil(sigil))
			expectError(t, err, InvalidExpression, fragment)
		}
		_, err := sb.MakeExpression(t.Context(), "1", WithSigil("@"))
		expectError(t, err, InvalidExpression, "jaml.WithSigil applies to MakeExpressionFromJaml")
	})
}

func TestASigilShowsInMessages(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: \"@{ 1 } and @{ 2 }\"\n", WithSigil("@"))
		expectError(t, err, InvalidExpression, "a value that starts with @{ must be one whole expression; interpolate into text with @{{ }}")
		_, err = Resolve[any](t.Context(), mustJaml(t, sb, "a: \"@{ nope }\"\n", WithSigil("@")))
		expectError(t, err, EvaluationFailed, "$.a: @{nope}: ReferenceError")
	})
}
