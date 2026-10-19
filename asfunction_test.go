package jaml

import (
	"testing"
	"time"
)

func TestAsFunctionAcceptsFunctionLiterals(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, source := range []string{
			"x => x.a",
			"(x, y) => { return {a: x.a, b: y} }",
			"function (x) { return x.a }",
			"function named(x) { return x.a }",
			"(x => x.a)",
			"((function (x) { return x.a }))",
			"/* leading */ x => x.a // trailing",
		} {
			compiled, err := sb.MakeExpression(t.Context(), source, AsFunction())
			if err != nil {
				t.Fatalf("MakeExpression(%q): %v", source, err)
			}
			var got any
			if err := resolveInto(t.Context(), &got, compiled, map[string]any{"a": 1}, 2); err != nil {
				t.Fatalf("Resolve(%q): %v", source, err)
			}
			if a, isMap := got.(map[string]any); isMap {
				got = a["a"]
			}
			expectEqual(t, got, any(float64(1)))
		}
	})
}

func TestAsFunctionRefusesEverythingElse(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		cases := map[string]string{
			"1 + 1":                    "not a function literal",
			"parseInt":                 "not a function literal",
			"Math.max":                 "not a function literal",
			"(() => x)()":              "not a function literal",
			"[1].map(x => x)":          "not a function literal",
			"({a: x => x})":            "not a function literal",
			"class { }":                "not a function literal",
			"(class A { })":            "not a function literal",
			"x => x, 1":                "not a function literal",
			"(x => x, y => y)":         "not a function literal",
			"cond ? x => x : y => y":   "not a function literal",
			"async x => x":             "an async function returns a promise, not data",
			"async function (x) { }":   "an async function returns a promise, not data",
			"async function* () { }":   "an async function returns a promise, not data",
			"function* () { yield 1 }": "a generator function returns an iterator, not data",
			"(function* named() { })":  "a generator function returns an iterator, not data",
			"x => ":                    "syntax error",
			"":                         "empty expression",
		}
		for source, fragment := range cases {
			_, err := sb.MakeExpression(t.Context(), source, AsFunction())
			expectError(t, err, InvalidExpression, fragment)
		}
	})
}

func TestAsFunctionEvaluatesNothing(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := within(t, 20*time.Second)
		_, err := sb.MakeExpression(ctx, "(() => { globalThis.fired = true; while (true) {} })()", AsFunction())
		expectError(t, err, InvalidExpression, "not a function literal")
		for _, source := range []string{
			"() => { while (true) {} }",
			"() => { const hoard = []; for (;;) hoard.push(new Array(1 << 20).fill(hoard.length)) }",
		} {
			if _, err := sb.MakeExpression(ctx, source, AsFunction()); err != nil {
				t.Fatalf("MakeExpression(%q): %v", source, err)
			}
		}
	}, WithBuildTimeout(2*time.Second))
}

func TestAsFunctionResolvesWithoutArgumentsToo(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		compiled, err := sb.MakeExpression(t.Context(), "x => x === undefined", AsFunction())
		if err != nil {
			t.Fatalf("MakeExpression: %v", err)
		}
		expectEqual(t, mustResolve[bool](t, t.Context(), compiled), true)
	})
}

func TestAsFunctionIsRefusedOnJaml(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: ${ x => x }\n", AsFunction())
		expectError(t, err, InvalidExpression, "jaml.AsFunction applies to MakeExpression, not to MakeExpressionFromJaml")
	})
}
