package jaml

import "testing"

func TestUndefinedIsStrictAndNullLooseByDefault(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var out map[string]any
		expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, "v: ${undefined}\n")), CoercionFailed, "$.v", "strict undefined")
		got := evalAs[map[string]any](t, mustJaml(t, sb, "v: ${null}\n"), nil)
		expectEqual(t, got, map[string]any{"v": nil})
	})
}

func TestLooseAbsenceDecodesAsNull(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type shape struct {
			Name  string   `json:"name"`
			Count *int     `json:"count"`
			Tags  []string `json:"tags"`
		}
		document := mustJaml(t, sb, "name: ${undefined}\ncount: ${null}\ntags: ${null}\n", WithUndefinedCoercion(Loose))
		expectEqual(t, mustResolve[shape](t, t.Context(), document), shape{})
	})
}

func TestStrictAbsenceRefusesExpressionResults(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: ${null}\n", WithNullCoercion(Strict))
		var out map[string]any
		expectError(t, resolveInto(t.Context(), &out, document), CoercionFailed, "$.a", "${null}", "strict null")
	})
}

func TestLiteralYAMLNullsAreNotExpressions(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: ~\nb: null\n", WithNullCoercion(Strict))
		expectEqual(t, evalAs[map[string]any](t, document, nil), map[string]any{"a": nil, "b": nil})
	})
}

func TestNullAndUndefinedAreConfiguredSeparately(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: ${null}\nb: ${undefined}\n", WithNullCoercion(Strict), WithUndefinedCoercion(Loose))
		var out map[string]any
		expectError(t, resolveInto(t.Context(), &out, document), CoercionFailed, "$.a", "strict null")
		looseNull := mustJaml(t, sb, "a: ${null}\nb: ${undefined}\n", WithNullCoercion(Loose), WithUndefinedCoercion(Strict))
		expectError(t, resolveInto(t.Context(), &out, looseNull), CoercionFailed, "$.b", "strict undefined")
	})
}

func TestInterpolatedAbsence(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		loose := mustJaml(t, sb, "v: \"[${{null}}|${{undefined}}]\"\n", WithUndefinedCoercion(Loose))
		expectEqual(t, mustResolve[string](t, t.Context(), loose.At("v")), "[|]")
		var out string
		strict := mustJaml(t, sb, "v: \"x ${{missing.name}}\"\n", WithUndefinedCoercion(Strict))
		expectError(t, resolveInto(valued(t.Context(), map[string]any{"missing": map[string]any{}}), &out, strict.At("v")), CoercionFailed, "$.v", "strict undefined")
		strictNull := mustJaml(t, sb, "v: \"x ${{null}}\"\n", WithNullCoercion(Strict))
		expectError(t, resolveInto(t.Context(), &out, strictNull.At("v")), CoercionFailed, "strict null")
	})
}

func TestNestedUndefinedFollowsJSON(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: \"${ ({a: undefined, b: [undefined, 1]}) }\"\n")
		expectEqual(t, evalAs[map[string]any](t, document, nil, "v"), map[string]any{"b": []any{nil, float64(1)}})
	})
}

func TestInvalidCoercionOptionsAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n", WithNullCoercion(Coercion(7)))
		expectError(t, err, InvalidExpression, "null coercion")
	})
}
