package jaml

import "testing"

type absentCase struct {
	name   string
	source string
}

var nullSources = []absentCase{
	{"tilde", "~"},
	{"empty", ""},
	{"null literal", "null"},
	{"null expression", "${null}"},
}

var undefinedSources = []absentCase{
	{"undefined expression", "${undefined}"},
	{"missing property", "${({}).missing}"},
	{"void", "${void 0}"},
}

func evalAbsent[T any](t *testing.T, source string, options ...Option) (T, error) {
	t.Helper()
	out := *new(T)
	template := mustTemplate(t, "v: "+source+"\n", options...)
	err := template.Eval(&out, nil, "v")
	return out, err
}

func TestNullDefaultsToLoose(t *testing.T) {
	for _, c := range nullSources {
		t.Run(c.name, func(t *testing.T) {
			got, err := evalAbsent[string](t, c.source)
			if err != nil {
				t.Fatal(err)
			}
			expectEqual(t, got, "")
		})
	}
}

func TestUndefinedDefaultsToStrict(t *testing.T) {
	for _, c := range undefinedSources {
		t.Run(c.name, func(t *testing.T) {
			_, err := evalAbsent[string](t, c.source)
			expectError(t, err, CoercionFailed, "$.v (line 1, column 4)", "strict undefined coercion refuses undefined for string")
		})
	}
}

func TestLooseAbsenceBecomesTheZeroValue(t *testing.T) {
	type pair struct{ A int }
	for _, c := range append(append([]absentCase{}, nullSources...), undefinedSources...) {
		t.Run(c.name, func(t *testing.T) {
			loose := []Option{WithNullCoercion(Loose), WithUndefinedCoercion(Loose)}
			text, err := evalAbsent[string](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, text, "")
			number, err := evalAbsent[int](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, number, 0)
			flag, err := evalAbsent[bool](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, flag, false)
			structure, err := evalAbsent[pair](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, structure, pair{})
			pointer, err := evalAbsent[*int](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, pointer, (*int)(nil))
			anything, err := evalAbsent[any](t, c.source, loose...)
			expectEqual(t, err, nil)
			expectEqual(t, anything, nil)
		})
	}
}

func TestLooseAbsenceOverwritesAnExistingValue(t *testing.T) {
	template := mustTemplate(t, "port: ~\n")
	out := struct {
		Port int `json:"port"`
	}{Port: 8080}
	if err := template.Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out.Port, 0)
}

func TestStrictNullRefusesOnlyNonNullableTargets(t *testing.T) {
	strict := WithNullCoercion(Strict)
	for _, c := range nullSources {
		t.Run(c.name, func(t *testing.T) {
			_, err := evalAbsent[string](t, c.source, strict)
			expectError(t, err, CoercionFailed, "strict null coercion refuses null for string")
			_, err = evalAbsent[int](t, c.source, strict)
			expectError(t, err, CoercionFailed, "for int")
			_, err = evalAbsent[struct{}](t, c.source, strict)
			expectError(t, err, CoercionFailed, "for struct {}")
			_, err = evalAbsent[[2]int](t, c.source, strict)
			expectError(t, err, CoercionFailed, "for [2]int")

			pointer, err := evalAbsent[*string](t, c.source, strict)
			expectEqual(t, err, nil)
			expectEqual(t, pointer, (*string)(nil))
			anything, err := evalAbsent[any](t, c.source, strict)
			expectEqual(t, err, nil)
			expectEqual(t, anything, nil)
			mapping, err := evalAbsent[map[string]int](t, c.source, strict)
			expectEqual(t, err, nil)
			expectEqual(t, mapping, map[string]int(nil))
			slice, err := evalAbsent[[]int](t, c.source, strict)
			expectEqual(t, err, nil)
			expectEqual(t, slice, []int(nil))
		})
	}
}

func TestStrictNullResetsANullableTargetToNil(t *testing.T) {
	template := mustTemplate(t, "v: ~\n", WithNullCoercion(Strict))
	existing := "kept"
	out := &existing
	if err := template.Eval(&out, nil, "v"); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, (*string)(nil))
}

func TestStrictUndefinedRefusesEveryTarget(t *testing.T) {
	for _, c := range undefinedSources {
		t.Run(c.name, func(t *testing.T) {
			_, err := evalAbsent[*string](t, c.source)
			expectError(t, err, CoercionFailed, "refuses undefined for *string")
			_, err = evalAbsent[any](t, c.source)
			expectError(t, err, CoercionFailed, "refuses undefined for interface {}")
			_, err = evalAbsent[map[string]any](t, c.source)
			expectError(t, err, CoercionFailed, "refuses undefined for map[string]interface {}")
		})
	}
}

func TestNullAndUndefinedAreConfiguredSeparately(t *testing.T) {
	options := []Option{WithNullCoercion(Strict), WithUndefinedCoercion(Loose)}
	_, err := evalAbsent[string](t, "~", options...)
	expectError(t, err, CoercionFailed, "strict null")
	got, err := evalAbsent[string](t, "${undefined}", options...)
	expectEqual(t, err, nil)
	expectEqual(t, got, "")
}

func TestNestedAbsenceFollowsTheSameRules(t *testing.T) {
	const source = "v: \"${ {a: null, b: undefined, c: [null, undefined]} }\"\n"
	looseBoth := []Option{WithNullCoercion(Loose), WithUndefinedCoercion(Loose)}

	loose := mustTemplate(t, source, looseBoth...)
	expectEqual(t, evalAs[map[string]any](t, loose, nil, "v"), map[string]any{"a": nil, "b": nil, "c": []any{nil, nil}})
	expectEqual(t, evalAs[string](t, loose, nil, "v"), `{"a":null,"b":null,"c":[null,null]}`)
	type shape struct {
		A string   `json:"a"`
		B int      `json:"b"`
		C []string `json:"c"`
	}
	expectEqual(t, evalAs[shape](t, loose, nil, "v"), shape{C: []string{"", ""}})

	defaults := mustTemplate(t, source)
	var anything any
	expectError(t, defaults.Eval(&anything, nil, "v"), CoercionFailed, "$.v.b (line 1, column 4)", "refuses undefined")
	var text string
	expectError(t, defaults.Eval(&text, nil, "v"), CoercionFailed, "$.v.b", "refuses undefined inside text")

	strictNull := mustTemplate(t, source, WithNullCoercion(Strict), WithUndefinedCoercion(Loose))
	var texts map[string]string
	expectError(t, strictNull.Eval(&texts, nil, "v"), CoercionFailed, "$.v.a", "refuses null for string")
	expectEqual(t, evalAs[string](t, strictNull, nil, "v"), `{"a":null,"b":null,"c":[null,null]}`)
}

func TestYAMLNullsInsideContainersFollowTheSameRules(t *testing.T) {
	template := mustTemplate(t, "v:\n  a: ~\n  list: [1, ~]\n", WithNullCoercion(Strict))
	var out map[string]any
	if err := template.Eval(&out, nil, "v"); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, map[string]any{"a": nil, "list": []any{int64(1), nil}})
	var ints struct {
		List []int `json:"list"`
		A    *int  `json:"a"`
	}
	expectError(t, template.Eval(&ints, nil, "v"), CoercionFailed, "$.v.list[1] (line 3, column 13)")
}

func TestInterpolatedAbsenceFollowsTheSameRules(t *testing.T) {
	loose := mustTemplate(t, "v: \"[${{null}}|${{undefined}}]\"\n", WithNullCoercion(Loose), WithUndefinedCoercion(Loose))
	expectEqual(t, evalAs[string](t, loose, nil, "v"), "[|]")

	strictNull := mustTemplate(t, "v: \"[${{null}}]\"\n", WithNullCoercion(Strict))
	var out string
	expectError(t, strictNull.Eval(&out, nil, "v"), CoercionFailed, "${{null}} is null, which strict null coercion refuses to interpolate")

	defaults := mustTemplate(t, "v: \"[${{missing.x}}]\"\n")
	expectError(t, defaults.Eval(&out, map[string]any{"missing": map[string]any{}}, "v"), CoercionFailed, "${{missing.x}} is undefined")
}
