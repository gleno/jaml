package jaml

import (
	"encoding/json"
	"testing"
)

type _Labelled struct {
	Label string `json:"label"`
}

var invalidUTF8Everywhere = map[string]any{
	"top level":         "x\xffy",
	"nested in array":   []any{"ok", []string{"\xfe"}},
	"map key":           map[string]int{"k\xff": 1},
	"struct field":      _Labelled{Label: "a\xc3"},
	"marshaled raw":     _WrittenJSON("\"\xff\""),
	"raw message raw":   json.RawMessage("[\"\xff\"]"),
	"cesu lone":         "\xed\xa0\x80",
	"cesu pair":         "\xed\xa0\xbd\xed\xb8\x80",
	"raw message cesu":  json.RawMessage("\"\xed\xa0\x80\""),
	"marshaled cesu":    _WrittenJSON("{\"\xed\xb0\x80\": 1}"),
	"nested struct key": map[string]_Labelled{"x": {Label: "\xff"}},
}

func TestInvalidUTF8IsRefusedInEveryPosition(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => typeof a")
		for name, value := range invalidUTF8Everywhere {
			t.Run(name, func(t *testing.T) {
				var _, err = Resolve[string](withValues(t, map[string]any{"bad": value}), compiled, 1)
				expectError(t, err, EvaluationFailed, `binding values: value "bad"`)
				_, err = Resolve[string](t.Context(), compiled, value)
				expectError(t, err, EvaluationFailed, "binding arguments: argument 1")
			})
		}
		var _, err = Resolve[string](withValues(t, map[string]any{"b\xffad": 1}), compiled, 1)
		expectError(t, err, EvaluationFailed, "is not named in valid UTF-8")
		_, err = Resolve[string](WithScript(t.Context(), "const s = '\xff'"), compiled, 1)
		expectError(t, err, InvalidExpression, "script 1", "Invalid UTF-8 character")
	})
}

func TestAReplacementEscapeWrittenAsJSONIsRefusedLikeInvalidUTF8(t *testing.T) {
	var escape = "\\ufffd"
	var replacement = "\ufffd"
	var written = map[string]any{
		"raw message":       json.RawMessage(`"` + escape + `"`),
		"raw message upper": json.RawMessage(`"\uFFFD"`),
		"marshaled key":     _WrittenJSON(`{"` + escape + `": 1}`),
		"marshaled text":    _WrittenJSON(`["a` + escape + `b"]`),
	}
	expectEscapeText(t, escape)
	for _, stdlib := range []struct {
		given any
		wrote string
	}{{written["raw message"], `"` + escape + `"`}, {"x\xff", `"x` + escape + `"`}} {
		encoded, err := wireJSON(stdlib.given)
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, string(encoded), stdlib.wrote)
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => [real, typed, a]")
		for name, value := range written {
			t.Run(name, func(t *testing.T) {
				var _, err = Resolve[[]string](withValues(t, map[string]any{"bad": value}), compiled, 1)
				expectError(t, err, EvaluationFailed, `binding values: value "bad" holds text that is not valid UTF-8`)
				_, err = Resolve[[]string](t.Context(), compiled, value)
				expectError(t, err, EvaluationFailed, "binding arguments: argument 1 holds text that is not valid UTF-8")
			})
		}
		var values = map[string]any{"real": replacement, "typed": escape}
		expectEqual(t, mustResolve[[]string](t, withValues(t, values), compiled, replacement), []string{replacement, escape, replacement})
	})
}
