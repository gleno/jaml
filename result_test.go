package jaml

import "testing"

func TestResultsMustBeYAMLData(t *testing.T) {
	cases := map[string]struct {
		source    string
		fragments []string
	}{
		"function":       {"${ () => 1 }", []string{"result is a function, which is not YAML data"}},
		"nested func":    {"${ [1, {f() {}}] }", []string{"result[1].f is a function"}},
		"symbol":         {"${ Symbol('s') }", []string{"result is a symbol"}},
		"nested symbol":  {"${ {s: Symbol.iterator} }", []string{"result.s is a symbol"}},
		"bigint":         {"${ 10n }", []string{"result is a"}},
		"nan":            {"${ 0/0 }", []string{"result is NaN"}},
		"infinity":       {"${ 1/0 }", []string{"result is Infinity"}},
		"neg infinity":   {"${ [-1/0] }", []string{"result[0] is -Infinity"}},
		"cyclic":         {"${ (() => { const o = {}; o.self = o; return o })() }", []string{"result.self is a cyclic reference"}},
		"cyclic list":    {"${ (() => { const a = []; a.push([a]); return a })() }", []string{"result[0][0] is a cyclic reference"}},
		"date":           {"${ new Date(0) }", []string{"result is an instance of Date"}},
		"regexp":         {"${ /x/ }", []string{"result is an instance of RegExp"}},
		"map":            {"${ new Map() }", []string{"result is an instance of Map"}},
		"set":            {"${ {s: new Set([1])} }", []string{"result.s is an instance of Set"}},
		"error":          {"${ new Error('e') }", []string{"result is an instance of Error"}},
		"promise":        {"${ Promise.resolve(1) }", []string{"result is an instance of Promise"}},
		"async":          {"${ (async () => 1)() }", []string{"result is an instance of Promise"}},
		"class instance": {"${ new (class Point { constructor() { this.x = 1 } })() }", []string{"result is an instance of Point"}},
		"boxed":          {"${ new String('s') }", []string{"result is an instance of String"}},
		"typed array":    {"${ new Uint8Array(2) }", []string{"result is a"}},
		"arguments":      {"${ (function() { return arguments })(1) }", []string{"result is an instance of Arguments"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			template := mustTemplate(t, "v: \""+c.source+"\"\n")
			var out any
			expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, append([]string{"$.v"}, c.fragments...)...)
		})
	}
}

func TestPlainDataResultsAreAccepted(t *testing.T) {
	template := mustTemplate(t, `
shared: "${ (() => { const x = {n: 1}; return [x, x] })() }"
nullProto: "${ Object.assign(Object.create(null), {a: 1}) }"
sparse: "${ [1, , 3] }"
negativeZero: ${ -0 }
json: "${ JSON.parse('{\"a\": [1, 2.5, \"x\", true, null]}') }"
getter: "${ ({ get g() { return 2 } }) }"
symbolKeysIgnored: "${ ({a: 1, [Symbol('s')]: 2}) }"
nonEnumerableIgnored: "${ Object.defineProperty({a: 1}, 'hidden', {value: 2}) }"
`, WithUndefinedCoercion(Loose))
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{
		"shared":               []any{map[string]any{"n": float64(1)}, map[string]any{"n": float64(1)}},
		"nullProto":            map[string]any{"a": float64(1)},
		"sparse":               []any{float64(1), nil, float64(3)},
		"negativeZero":         float64(0),
		"json":                 map[string]any{"a": []any{float64(1), 2.5, "x", true, nil}},
		"getter":               map[string]any{"g": float64(2)},
		"symbolKeysIgnored":    map[string]any{"a": float64(1)},
		"nonEnumerableIgnored": map[string]any{"a": float64(1)},
	})
}

func TestObjectsInterpolateAsJSON(t *testing.T) {
	template := mustTemplate(t, "v: \"is ${{ ({z: 1, a: [2]}) }}\"\n")
	expectEqual(t, evalAs[string](t, template, nil, "v"), `is {"a":[2],"z":1}`)
}

func TestHugeResultsAreRefused(t *testing.T) {
	template := mustTemplate(t, "v: \"${ (() => { let a = [1]; for (let i = 0; i < 30; i++) a = [a, a]; return a })() }\"\n")
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "holds more than")
	long := mustTemplate(t, "v: \"${ (() => { const a = []; a.length = 1e9; return a })() }\"\n")
	expectError(t, long.Eval(&out, nil, "v"), EvaluationFailed, "result holds more than")
}

func TestThrowingGettersFailTheEvaluation(t *testing.T) {
	template := mustTemplate(t, "v: \"${ ({ get g() { throw new TypeError('nope') } }) }\"\n")
	var out any
	expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, "TypeError: nope")
}

func TestRuntimeErrorsNameTheExpression(t *testing.T) {
	cases := map[string]struct {
		source    string
		fragments []string
	}{
		"reference":     {"${ missing + 1 }", []string{"${missing + 1}", "ReferenceError: missing is not defined"}},
		"type":          {"${ null.x }", []string{"TypeError"}},
		"thrown error":  {"${ (() => { throw new RangeError('too far') })() }", []string{"RangeError: too far"}},
		"thrown string": {"${ (() => { throw 'plain' })() }", []string{"threw plain"}},
		"thrown object": {"${ (() => { throw {code: 1} })() }", []string{"threw [object Object]"}},
		"bad toString":  {"${ (() => { throw { get message() { throw 1 } } })() }", []string{"threw a value whose message cannot be read"}},
		"interpolated":  {"x ${{ missing }}", []string{"${{missing}}", "ReferenceError"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			template := mustTemplate(t, "v: \""+c.source+"\"\n")
			var out any
			expectError(t, template.Eval(&out, nil, "v"), EvaluationFailed, append([]string{"$.v"}, c.fragments...)...)
		})
	}
}
