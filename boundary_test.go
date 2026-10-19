package jaml

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestANilOptionIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeTemplate(t.Context(), "a: 1\n", WithNullCoercion(Loose), nil)
		expectError(t, err, InvalidTemplate, "option 2 is nil")
	})
}

func TestCoercionsAreCheckedNullFirst(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for range 64 {
			_, err := sb.MakeTemplate(t.Context(), "a: 1\n", WithUndefinedCoercion(Coercion(8)), WithNullCoercion(Coercion(7)))
			expectError(t, err, InvalidTemplate, "null coercion", "got 7")
		}
	})
}

type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("refuses")
}

type countingMarshaler struct{ calls *int }

func (c countingMarshaler) MarshalJSON() ([]byte, error) {
	*c.calls++
	return []byte("1"), nil
}

func TestBadVarsAreRefusedBeforeRendering(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		cyclic := map[string]any{}
		cyclic["self"] = cyclic
		cases := map[string]struct {
			value    any
			fragment string
		}{
			"channel":         {make(chan int), `var "bad" is not JSON data`},
			"nan":             {math.NaN(), `var "bad" is not JSON data`},
			"cyclic":          {cyclic, `var "bad" is not JSON data`},
			"failing marshal": {failingMarshaler{}, `var "bad" is not JSON data`},
			"too deep":        {nestedVar(4097), `var "bad" nests deeper than 4096`},
		}
		template := mustTemplate(t, sb, "plain: 1\nz: ${ 1 }\n")
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				vars := map[string]any{"bad": c.value}
				var out any
				for _, path := range [][]string{{"plain"}, nil} {
					err := template.Eval(t.Context(), &out, vars, path...)
					expectError(t, err, EvaluationFailed, c.fragment)
					if strings.Contains(err.Error(), "$") {
						t.Fatalf("a var error blames a template path: %q", err)
					}
				}
			})
		}
	})
}

func TestVarsAreEncodedOncePerEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		calls := 0
		template := mustTemplate(t, sb, "a: ${ v }\nb: ${ v + 1 }\nc: \"${{ v }}\"\n")
		evalAs[map[string]any](t, template, map[string]any{"v": countingMarshaler{calls: &calls}})
		expectEqual(t, calls, 1)
	})
}

func TestListStepsAreCanonicalIndexes(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "l: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10]\n")
		expectEqual(t, evalAs[int](t, template, nil, "l", "0"), 0)
		expectEqual(t, evalAs[int](t, template, nil, "l", "1"), 1)
		expectEqual(t, evalAs[int](t, template, nil, "l", "10"), 10)
		for _, step := range []string{"+1", "01", "-0", " 1", "1 ", "00", ""} {
			t.Run(step, func(t *testing.T) {
				var out any
				expectError(t, template.Eval(t.Context(), &out, nil, "l", step), MissingPath, "is a list of 11")
			})
		}
	})
}
