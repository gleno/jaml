package jaml

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
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

func TestBadVarsAreRefusedOnTemplatesWithoutExpressionsInBothModes(t *testing.T) {
	deep := any(1)
	for range maxDataDepth + 1 {
		deep = []any{deep}
	}
	cases := map[string]struct {
		vars     map[string]any
		fragment string
	}{
		"not JSON data":  {map[string]any{"bad": func() {}}, `binding vars: var "bad" is not JSON data`},
		"too deep":       {map[string]any{"bad": deep}, `var "bad"`},
		"invalid UTF-8":  {map[string]any{"bad": "x\xffy"}, `binding vars: var "bad" holds text that is not valid UTF-8`},
		"name not UTF-8": {map[string]any{"b\xffad": 1}, "is not named in valid UTF-8"},
		"global name":    {map[string]any{"Math": 1}, `binding vars: var "Math" collides with a JavaScript global`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			both := agreement{text: "a: 1\nb: [x, y]\nc: {d: e}\n", vars: c.vars}.eval(t, intoAny)
			both.expectRefused(t, EvaluationFailed, c.fragment)
			expectNoTemplatePath(t, both.trusted)
		})
	}
}

func TestEveryGlobalOfTheEngineIsRefusedAsAVarName(t *testing.T) {
	template := mustTemplate(t, "plain: 1\n")
	for _, name := range goja.New().GlobalObject().GetOwnPropertyNames() {
		var out any
		expectError(t, template.Eval(&out, map[string]any{name: 1}), EvaluationFailed, `var "`+name+`" collides with a JavaScript global`)
	}
}

func TestVarsBindOnceTheFirstExpressionNeedsThem(t *testing.T) {
	template := mustTemplate(t, "plain: [1, 2]\nlater: ${ answer + 1 }\n")
	expectEqual(t, evalAs[map[string]any](t, template, map[string]any{"answer": 41}), map[string]any{"plain": []any{float64(1), float64(2)}, "later": float64(42)})
}
