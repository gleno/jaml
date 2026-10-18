package jaml

import "testing"

func TestUndefinedIsStrictAndNullLooseByDefault(t *testing.T) {
	var out map[string]any
	expectError(t, mustTemplate(t, "v: ${undefined}\n").Eval(&out, nil), CoercionFailed, "$.v", "strict undefined")
	got := evalAs[map[string]any](t, mustTemplate(t, "v: ${null}\n"), nil)
	expectEqual(t, got, map[string]any{"v": nil})
}

func TestLooseAbsenceDecodesAsNull(t *testing.T) {
	type shape struct {
		Name  string   `json:"name"`
		Count *int     `json:"count"`
		Tags  []string `json:"tags"`
	}
	template := mustTemplate(t, "name: ${undefined}\ncount: ${null}\ntags: ${null}\n", WithUndefinedCoercion(Loose))
	expectEqual(t, evalAs[shape](t, template, nil), shape{})
}

func TestStrictAbsenceRefusesExpressionResults(t *testing.T) {
	template := mustTemplate(t, "a: ${null}\n", WithNullCoercion(Strict))
	var out map[string]any
	expectError(t, template.Eval(&out, nil), CoercionFailed, "$.a", "${null}", "strict null")
}

func TestLiteralYAMLNullsAreNotExpressions(t *testing.T) {
	template := mustTemplate(t, "a: ~\nb: null\n", WithNullCoercion(Strict))
	expectEqual(t, evalAs[map[string]any](t, template, nil), map[string]any{"a": nil, "b": nil})
}

func TestNullAndUndefinedAreConfiguredSeparately(t *testing.T) {
	template := mustTemplate(t, "a: ${null}\nb: ${undefined}\n", WithNullCoercion(Strict), WithUndefinedCoercion(Loose))
	var out map[string]any
	expectError(t, template.Eval(&out, nil), CoercionFailed, "$.a", "strict null")
	looseNull := mustTemplate(t, "a: ${null}\nb: ${undefined}\n", WithNullCoercion(Loose), WithUndefinedCoercion(Strict))
	expectError(t, looseNull.Eval(&out, nil), CoercionFailed, "$.b", "strict undefined")
}

func TestInterpolatedAbsence(t *testing.T) {
	loose := mustTemplate(t, "v: \"[${{null}}|${{undefined}}]\"\n", WithUndefinedCoercion(Loose))
	expectEqual(t, evalAs[string](t, loose, nil, "v"), "[|]")
	var out string
	strict := mustTemplate(t, "v: \"x ${{missing.name}}\"\n", WithUndefinedCoercion(Strict))
	expectError(t, strict.Eval(&out, map[string]any{"missing": map[string]any{}}, "v"), CoercionFailed, "$.v", "strict undefined")
	strictNull := mustTemplate(t, "v: \"x ${{null}}\"\n", WithNullCoercion(Strict))
	expectError(t, strictNull.Eval(&out, nil, "v"), CoercionFailed, "strict null")
}

func TestNestedUndefinedFollowsJSON(t *testing.T) {
	template := mustTemplate(t, "v: \"${ ({a: undefined, b: [undefined, 1]}) }\"\n")
	expectEqual(t, evalAs[map[string]any](t, template, nil, "v"), map[string]any{"b": []any{nil, float64(1)}})
}

func TestInvalidCoercionOptionsAreRefused(t *testing.T) {
	_, err := MakeTrustedTemplate("a: 1\n", WithNullCoercion(Coercion(7)))
	expectError(t, err, InvalidTemplate, "null coercion")
}
