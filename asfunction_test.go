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
			compiled, err := sb.Compile(t.Context(), source, AsFunction())
			if err != nil {
				t.Fatalf("Compile(%q): %v", source, err)
			}
			var got any
			if err := compiled.Call(t.Context(), &got, map[string]any{"a": 1}, 2); err != nil {
				t.Fatalf("Call(%q): %v", source, err)
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
			_, err := sb.Compile(t.Context(), source, AsFunction())
			expectError(t, err, InvalidTemplate, fragment)
		}
	})
}

func TestAsFunctionEvaluatesNothing(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := within(t, 20*time.Second)
		_, err := sb.Compile(ctx, "(() => { globalThis.fired = true; while (true) {} })()", AsFunction())
		expectError(t, err, InvalidTemplate, "not a function literal")
		for _, source := range []string{
			"() => { while (true) {} }",
			"() => { const hoard = []; for (;;) hoard.push(new Array(1 << 20).fill(hoard.length)) }",
		} {
			if _, err := sb.Compile(ctx, source, AsFunction()); err != nil {
				t.Fatalf("Compile(%q): %v", source, err)
			}
		}
	}, WithBuildTimeout(2*time.Second))
}

func TestAsFunctionLeavesNoWayToEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		compiled, err := sb.Compile(t.Context(), "x => 1", AsFunction())
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		var out any
		expectError(t, compiled.Eval(t.Context(), &out, nil), EvaluationFailed, "$: x => 1: result is a function")
	})
}

func TestAsFunctionIsRefusedForTemplates(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeTemplate(t.Context(), "a: ${ x => x }\n", AsFunction())
		expectError(t, err, InvalidTemplate, "jaml.AsFunction applies to Compile, not to templates")
	})
}
