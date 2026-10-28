package jaml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"testing"
	"unicode/utf8"
)

type _Labelled struct {
	Label string `json:"label"`
}

type _TextOf string

func (t _TextOf) MarshalText() ([]byte, error) {
	return []byte(t), nil
}

type _TextKey struct {
	text string
}

func (k _TextKey) MarshalText() ([]byte, error) {
	return []byte(k.text), nil
}

type _Misnamed struct {
	Label string "json:\"l\xffabel\""
}

type _Embedding struct {
	_Labelled
	Other string `json:"other"`
}

type _Hidden struct {
	Shown  string `json:"shown"`
	Hidden string `json:"-"`
	hidden string
}

func pointerTo[T any](value T) *T {
	return &value
}

var invalidUTF8Everywhere = map[string]any{
	"top level":          "x\xffy",
	"nested in array":    []any{"ok", []string{"\xfe"}},
	"map key":            map[string]int{"k\xff": 1},
	"struct field":       _Labelled{Label: "a\xc3"},
	"marshaled raw":      _WrittenJSON("\"\xff\""),
	"raw message raw":    jsontext.Value("[\"\xff\"]"),
	"cesu lone":          "\xed\xa0\x80",
	"cesu pair":          "\xed\xa0\xbd\xed\xb8\x80",
	"raw message cesu":   jsontext.Value("\"\xed\xa0\x80\""),
	"marshaled cesu":     _WrittenJSON("{\"\xed\xb0\x80\": 1}"),
	"nested struct key":  map[string]_Labelled{"x": {Label: "\xff"}},
	"text marshaler":     []any{_TextOf("a\xff")},
	"text marshaler key": map[_TextKey]int{{text: "k\xfe"}: 1},
	"pointer":            pointerTo("p\xff"),
	"array":              [2]string{"ok", "\xff"},
	"embedded field":     _Embedding{_Labelled: _Labelled{Label: "\xff"}},
	"deep":               map[string][]map[string]*_Labelled{"a": {{"b": {Label: "\xc3"}}}},
}

var validTextEverywhere = map[string]any{
	"bytes":              []byte{0xff, 0xfe},
	"replacement":        _Labelled{Label: string(utf8.RuneError)},
	"text marshaler key": map[_TextKey]int{{text: "k"}: 1},
	"hidden fields":      _Hidden{Shown: "ok", Hidden: "\xff", hidden: "\xfe"},
	"nil pointer":        (*_Labelled)(nil),
}

func TestInvalidUTF8IsRefusedInEveryPosition(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => typeof a")
		for name, value := range invalidUTF8Everywhere {
			t.Run(name, func(t *testing.T) {
				var _, err = Resolve[string](withValues(t, map[string]any{"bad": value}), compiled, 1)
				expectError(t, err, InvalidInput, `binding values: value "bad" holds text that is not valid UTF-8`)
				_, err = Resolve[string](t.Context(), compiled, value)
				expectError(t, err, InvalidInput, "binding arguments: argument 1 holds text that is not valid UTF-8")
			})
		}
		var _, err = Resolve[string](withValues(t, map[string]any{"b\xffad": 1}), compiled, 1)
		expectError(t, err, InvalidInput, "is not named in valid UTF-8")
		_, err = Resolve[string](WithScript(t.Context(), "const s = '\xff'"), compiled, 1)
		expectError(t, err, InvalidExpression, "script 1", "Invalid UTF-8 character")
	})
}

func TestValidTextIsAdmittedInEveryPosition(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => typeof a")
		for name, value := range validTextEverywhere {
			t.Run(name, func(t *testing.T) {
				mustResolve[string](t, withValues(t, map[string]any{"good": value}), compiled, value)
			})
		}
	})
}

func TestAReplacementEscapeWrittenAsJSONUsesNativeSemantics(t *testing.T) {
	var escape = "\\ufffd"
	var replacement = "\ufffd"
	var written = map[string]any{
		"raw message":       jsontext.Value(`"` + escape + `"`),
		"raw message upper": jsontext.Value(`"\uFFFD"`),
		"marshaled key":     _WrittenJSON(`{"` + escape + `": 1}`),
		"marshaled text":    _WrittenJSON(`["a` + escape + `b"]`),
	}
	expectEscapeText(t, escape)
	if err := unboundedWriter().quoted("x\xff"); !errors.Is(err, errInvalidText) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => a")
		for name, value := range written {
			t.Run(name, func(t *testing.T) {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var want any
				if err := json.Unmarshal(encoded, &want); err != nil {
					t.Fatal(err)
				}
				expectEqual(t, mustResolve[any](t, t.Context(), compiled, value), want)
				expectEqual(t, mustResolve[any](t, withValues(t, map[string]any{"value": value}), mustExpression(t, sb, "value")), want)
			})
		}
		expectEqual(t, mustResolve[string](t, t.Context(), compiled, replacement), replacement)
	})
}
