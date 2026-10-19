package jaml

import (
	"encoding/json"
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

func TestVarsNamedLikeJavaScriptGlobalsAreRefusedInBothKinds(t *testing.T) {
	for _, name := range []string{"JSON", "Error", "Array", "Math", "globalThis", "undefined", "NaN", "Infinity", "eval", "Function", "Object", "parseInt", "constructor", "toString", "toLocaleString", "valueOf", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "__proto__"} {
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

func TestVarsHoldingInvalidUTF8AreRefusedInBothKinds(t *testing.T) {
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
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		vars := map[string]any{"real": "a�b", "written": `�`, "key": map[string]any{`�`: "�"}}
		got := agreement{text: "v: ${ [real, written, Object.keys(key)[0]] }\n", vars: vars, path: []string{"v"}}
		got.eval(t, intoAny).expectAccepted(t)
		expectEqual(t, evalAs[[]string](t, mustTemplate(t, sb, "v: ${ [real, written, Object.keys(key)[0]] }\n"), vars, "v"), []string{"a�b", `�`, `�`})
	})
}

func TestBadVarsAreRefusedOnTemplatesWithoutExpressionsInBothKinds(t *testing.T) {
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
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "plain: 1\n")
		for _, name := range goja.New().GlobalObject().GetOwnPropertyNames() {
			var out any
			expectError(t, template.Eval(t.Context(), &out, map[string]any{name: 1}), EvaluationFailed, `var "`+name+`" collides with a JavaScript global`)
		}
	})
}

func TestVarsBindOnceTheFirstExpressionNeedsThem(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "plain: [1, 2]\nlater: ${ answer + 1 }\n")
		expectEqual(t, evalAs[map[string]any](t, template, map[string]any{"answer": 41}), map[string]any{"plain": []any{float64(1), float64(2)}, "later": float64(42)})
	})
}

type writtenJSON string

func (w writtenJSON) MarshalJSON() ([]byte, error) {
	return []byte(w), nil
}

var textThatIsNotUTF8 = map[string]any{
	"escaped lone high":      json.RawMessage(`"\ud800"`),
	"escaped lone low":       json.RawMessage(`"a\udc00b"`),
	"escaped reversed pair":  json.RawMessage(`"\udc00\ud800"`),
	"escaped lone key":       json.RawMessage(`{"\ud800": 1}`),
	"marshaled lone":         writtenJSON(`["\udfff"]`),
	"raw invalid byte":       json.RawMessage("\"\xff\""),
	"raw invalid key":        json.RawMessage("{\"\xfe\": 1}"),
	"marshaled invalid byte": writtenJSON("{\"k\": \"a\xc3\"}"),
}

func TestVarsWrittenAsTextThatIsNotUTF8AreRefusedInBothKinds(t *testing.T) {
	for name, value := range textThatIsNotUTF8 {
		t.Run(name, func(t *testing.T) {
			both := agreement{text: "v: ${ typeof bad }\n", vars: map[string]any{"bad": value}}.eval(t, intoAny)
			both.expectRefused(t, EvaluationFailed, `binding vars: var "bad" holds text that is not valid UTF-8`)
		})
	}
}

func TestArgumentsWrittenAsTextThatIsNotUTF8AreRefusedInBothKinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		compiled := mustCompile(t, sb, "(a, b) => typeof b")
		for name, value := range textThatIsNotUTF8 {
			t.Run(name, func(t *testing.T) {
				var out any
				expectError(t, compiled.Call(t.Context(), &out, 1, value), EvaluationFailed, `binding arguments: argument 2 holds text that is not valid UTF-8`)
			})
		}
	})
}

func TestWellFormedWrittenTextStillBinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		compiled := mustCompile(t, sb, "x => [x.pair, x.pair.length, x.real, x.plain]")
		var out []any
		value := writtenJSON(`{"pair": "\ud83d\ude00", "real": "` + "\u00e9\ufffd" + `", "plain": "\u0041"}`)
		if err := compiled.Call(t.Context(), &out, value); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, out, []any{"\U0001F600", float64(2), "\u00e9\ufffd", "A"})
	})
}

func TestTheChildRefusesWorkThatWasNeverAdmitted(t *testing.T) {
	for name, written := range map[string]string{
		"inherited name":   `{"vars": {"hasOwnProperty": 1}, "pinned": {}}`,
		"own global name":  `{"vars": {"JSON": 1}, "pinned": {}}`,
		"lone surrogate":   `{"vars": {"a": "\ud800"}, "pinned": {}}`,
		"raw invalid byte": "{\"vars\": {\"a\": \"\xff\"}, \"pinned\": {}}",
		"too deep":         `{"vars": {"a": ` + nestedJSONText(4097) + `}, "pinned": {}}`,
		"lone argument":    `{"call": {"args": [1, "\udc00"]}, "pinned": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var decoded work
			if err := json.Unmarshal([]byte(written), &decoded); err == nil {
				t.Fatalf("the child bound unchecked work %s", written)
			}
		})
	}
	var decoded work
	if err := json.Unmarshal([]byte(`{"vars": {"a": "\ud83d\ude00"}, "call": {"args": [{"k": [1]}]}, "pinned": {}}`), &decoded); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, string(decoded.Vars["a"].encoded)+string(decoded.Call.Args[0].encoded), `"\ud83d\ude00"{"k": [1]}`)
}
