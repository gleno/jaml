package jaml

import (
	"errors"
	"math"
	"testing"
)

func TestANilOptionIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n", WithNullCoercion(Loose), nil)
		expectError(t, err, InvalidExpression, "option 2 is nil")
	})
}

func TestCoercionsAreCheckedNullFirst(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for range 64 {
			_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n", WithUndefinedCoercion(Coercion(8)), WithNullCoercion(Coercion(7)))
			expectError(t, err, InvalidExpression, "null coercion", "got 7")
		}
	})
}

type _FailingMarshaler struct{}

func (_FailingMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("refuses")
}

type _CountingMarshaler struct{ calls *int }

func (c _CountingMarshaler) MarshalJSON() ([]byte, error) {
	*c.calls++
	return []byte("1"), nil
}

func TestBadVarsAreRefusedBeforeRendering(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var cyclic = map[string]any{}
		cyclic["self"] = cyclic
		var cases = map[string]struct {
			value    any
			fragment string
		}{
			"channel":         {make(chan int), `value "bad" is not JSON data`},
			"nan":             {math.NaN(), `value "bad" is not JSON data`},
			"cyclic":          {cyclic, `value "bad" is not JSON data`},
			"failing marshal": {_FailingMarshaler{}, `value "bad" is not JSON data`},
			"too deep":        {nestedVar(4097), `value "bad" nests deeper than 4096`},
		}
		var document = mustJaml(t, sb, "plain: 1\nz: ${ 1 }\n")
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				var vars = map[string]any{"bad": c.value}
				var out any
				for _, path := range [][]string{{"plain"}, nil} {
					var err = resolveInto(valued(t.Context(), vars), &out, document.At(path...))
					expectError(t, err, EvaluationFailed, c.fragment)
					expectNoTemplatePath(t, err)
				}
			})
		}
	})
}

func TestVarsAreEncodedOncePerEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var calls = 0
		var document = mustJaml(t, sb, "a: ${ v }\nb: ${ v + 1 }\nc: \"${{ v }}\"\n")
		evalAs[map[string]any](t, document, map[string]any{"v": _CountingMarshaler{calls: &calls}})
		expectEqual(t, calls, 1)
	})
}

func TestListStepsAreCanonicalIndexes(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "l: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10]\n")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("l", "0")), 0)
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("l", "1")), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("l", "10")), 10)
		for _, step := range []string{"+1", "01", "-0", " 1", "1 ", "00", ""} {
			t.Run(step, func(t *testing.T) {
				var out any
				expectError(t, resolveInto(t.Context(), &out, document.At("l", step)), MissingPath, "is a list of 11")
			})
		}
	})
}
