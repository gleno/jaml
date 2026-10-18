package jaml

import (
	"strings"
	"testing"
)

func expectNoTemplatePath(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "$") {
		t.Fatalf("a var error blames a template path: %q", err)
	}
}

func TestVarsNamedLikeJavaScriptGlobalsAreRefusedInBothModes(t *testing.T) {
	for _, name := range []string{"JSON", "Error", "Array", "Math", "globalThis", "undefined", "NaN", "Infinity", "eval", "Function", "Object", "parseInt"} {
		t.Run(name, func(t *testing.T) {
			for _, text := range []string{"a: ${ 1 }\nb: ${ 2 }\n", "plain: 1\n"} {
				both := agreement{text: text, vars: map[string]any{name: 7, "fine": 1}}.eval(t, intoAny)
				both.expectRefused(t, EvaluationFailed, `binding vars: var "`+name+`" collides with a JavaScript global`)
				expectNoTemplatePath(t, both.trusted)
			}
		})
	}
}

func TestVarsNamedLikeOrdinaryWordsStillBind(t *testing.T) {
	agreement{text: "v: ${ [name, json, error, globalthis].join() }\n", vars: map[string]any{"name": "a", "json": "b", "error": "c", "globalthis": "d"}}.eval(t, intoAny).expectAccepted(t)
}

func TestVarsHoldingInvalidUTF8AreRefusedInBothModes(t *testing.T) {
	cases := map[string]map[string]any{
		"value":      {"bad": "x\xffy"},
		"nested key": {"bad": map[string]any{"k\xfe": 1}},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			both := agreement{text: "v: ${ 1 }\n", vars: vars}.eval(t, intoAny)
			both.expectRefused(t, EvaluationFailed, `binding vars: var "bad" holds text that is not valid UTF-8`)
			expectNoTemplatePath(t, both.trusted)
		})
	}
	both := agreement{text: "v: ${ 1 }\n", vars: map[string]any{"b\xffad": 1}}.eval(t, intoAny)
	both.expectRefused(t, EvaluationFailed, "is not named in valid UTF-8")
}

func TestRealReplacementCharactersAndEscapedTextStillBind(t *testing.T) {
	vars := map[string]any{"real": "a�b", "written": `�`, "key": map[string]any{`�`: "�"}}
	got := agreement{text: "v: ${ [real, written, Object.keys(key)[0]] }\n", vars: vars, path: []string{"v"}}
	got.eval(t, intoAny).expectAccepted(t)
	expectEqual(t, evalAs[[]string](t, mustTemplate(t, "v: ${ [real, written, Object.keys(key)[0]] }\n"), vars, "v"), []string{"a�b", `�`, `�`})
}
