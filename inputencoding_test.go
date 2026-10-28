package jaml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type _InputText struct {
	calls *int
	first error
}

func (v _InputText) MarshalText() ([]byte, error) {
	*v.calls++
	if *v.calls == 1 && v.first != nil {
		return nil, v.first
	}
	return []byte(strconv.Itoa(*v.calls)), nil
}

type _InputPointerText struct{ calls *int }

func (v *_InputPointerText) MarshalText() ([]byte, error) {
	*v.calls++
	return []byte(strconv.Itoa(*v.calls)), nil
}

type _OmittedInputText string

func (_OmittedInputText) MarshalText() ([]byte, error) { return []byte("\xff"), nil }

type _InputJSONTo struct {
	Text  string
	calls *int
}

func (v _InputJSONTo) MarshalJSONTo(out *jsontext.Encoder) error {
	*v.calls++
	return out.WriteToken(jsontext.String(strconv.Itoa(*v.calls)))
}

type _InputTextAppender struct {
	Text  string
	calls *int
}

func (v _InputTextAppender) AppendText(out []byte) ([]byte, error) {
	*v.calls++
	return strconv.AppendInt(out, int64(*v.calls), 10), nil
}

func TestInputTextCallbacksFollowTheJSONEncoder(t *testing.T) {
	for name, makeValue := range map[string]func(*int) any{
		"value":   func(calls *int) any { return _InputText{calls: calls} },
		"pointer": func(calls *int) any { return &_InputPointerText{calls: calls} },
		"addressable field": func(calls *int) any {
			return &struct{ Text _InputPointerText }{Text: _InputPointerText{calls: calls}}
		},
		"nonaddressable field": func(calls *int) any {
			return struct{ Text _InputPointerText }{Text: _InputPointerText{calls: calls}}
		},
		"field order": func(calls *int) any {
			return struct{ First, Second _InputText }{_InputText{calls: calls}, _InputText{calls: calls}}
		},
		"shared pointer": func(calls *int) any {
			var shared = &_InputPointerText{calls: calls}
			return []*_InputPointerText{shared, shared}
		},
		"map key": func(calls *int) any { return map[_InputText]int{{calls: calls}: 7} },

		"JSON stream method": func(calls *int) any { return _InputJSONTo{Text: "\xff", calls: calls} },
		"text append method": func(calls *int) any { return _InputTextAppender{Text: "\xff", calls: calls} },
		"text append map key": func(calls *int) any {
			return map[_InputTextAppender]int{{Text: "\xff", calls: calls}: 7}
		},
		"omitted field": func(*int) any {
			return struct {
				Text _OmittedInputText `json:"text,omitzero"`
			}{}
		},
		"nil pointer": func(*int) any { return (*_InputPointerText)(nil) },
		"string escaping": func(*int) any {
			return map[string]string{"<key>": "<&>\u2028\u2029\n"}
		},
		"nil containers": func(*int) any {
			return struct {
				List []int
				Map  map[int]string
			}{}
		},
		"byte slice and array": func(*int) any {
			return struct {
				Bytes []byte
				Array [2]byte
			}{[]byte{0xff, 0xfe}, [2]byte{0xff, 0xfe}}
		},
		"field options": func(*int) any {
			return struct {
				Name   string `json:"name"`
				Number int    `json:"n,string"`
				Omit   string `json:"omitted,omitempty"`
			}{Name: "label", Number: 7}
		},
		"integer map keys": func(*int) any { return map[int]string{2: "two", 1: "one"} },
		"raw JSON":         func(*int) any { return jsontext.Value(` { "x": "\u00e9", "y": "<&>" } `) },
	} {
		t.Run(name, func(t *testing.T) {
			var nativeCalls, inputCalls int
			want, err := wireJSON(makeValue(&nativeCalls))
			if err != nil {
				t.Fatal(err)
			}
			var writer = unboundedWriter()
			if _, err := writer.admitted("input", makeValue(&inputCalls)); err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal(writer.written, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			expectEqual(t, gotValue, wantValue)
			expectEqual(t, inputCalls, nativeCalls)
		})
	}
}

func TestInputTextErrorsAreNotConsumedByValidation(t *testing.T) {
	// This is an application error, not the encoder's typed invalid-UTF8 error.
	var first = errors.New("invalid UTF-8")
	for name, makeValue := range map[string]func(*int) any{
		"value":   func(calls *int) any { return _InputText{calls: calls, first: first} },
		"map key": func(calls *int) any { return map[_InputText]int{{calls: calls, first: first}: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			_, err := writeWork(_Ask{args: []any{makeValue(&calls)}}, unboundedAsk)
			if !errors.Is(err, InvalidInput) || !errors.Is(err, first) {
				t.Fatalf("first callback error was lost: %v", err)
			}
			expectEqual(t, calls, 1)
		})
	}
}

func TestInputTextIsEncodedOnceForEachBinding(t *testing.T) {
	eachKind(t, func(t *testing.T, sandbox *Sandbox) {
		var expression = mustExpression(t, sandbox, "a => [v, a]")
		var calls int
		var value = _InputText{calls: &calls}
		got, err := Resolve[[]string](valued(t.Context(), map[string]any{"v": value}), expression, value)
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, got, []string{"1", "2"})
		expectEqual(t, calls, 2)
	})
}

func TestInputCapDiagnosticsDoNotEncodeValuesAgain(t *testing.T) {
	var before, after int
	var args = []any{_CountingMarshaler{calls: &before}, strings.Repeat("x", 1024), _CountingMarshaler{calls: &after}}
	_, err := writeWork(_Ask{args: args}, 128)
	expectError(t, err, InvalidInput, "the request is larger than 128 bytes while writing argument 2")
	expectEqual(t, before, 1)
	expectEqual(t, after, 0)
}

func TestInputCapDiagnosticsDoNotAllocateTheRefusedEncoding(t *testing.T) {
	var oversized = strings.Repeat("x", 16<<20)
	var err error
	var allocated = allocatedIn(func() {
		_, err = writeWork(_Ask{args: []any{oversized}}, 128)
	})
	if !errors.Is(err, InvalidInput) {
		t.Fatalf("oversized request: %v", err)
	}
	if allocated > 1<<20 {
		t.Fatalf("refusing a 16 MiB string with a 128-byte cap allocated %d bytes", allocated)
	}
	t.Logf("refusal allocated %d bytes for a 16 MiB input", allocated)
}

type _FallbackInputJSON struct {
	Text  string
	calls *int
}

func (v _FallbackInputJSON) MarshalJSONTo(*jsontext.Encoder) error {
	*v.calls++
	return errors.ErrUnsupported
}

type _MalformedInputJSON struct {
	Text    string "json:\"l\xffabel\""
	decline bool
	calls   *int
}

func (v _MalformedInputJSON) MarshalJSONTo(out *jsontext.Encoder) error {
	*v.calls++
	if v.decline {
		return errors.ErrUnsupported
	}
	return out.WriteToken(jsontext.String("ok"))
}

func TestInputStreamMethodFallbackPreservesTextAndTagChecks(t *testing.T) {
	for _, content := range []string{"valid", "\xff"} {
		t.Run(content, func(t *testing.T) {
			var calls int
			var writer = unboundedWriter()
			_, err := writer.admitted("input", _FallbackInputJSON{Text: content, calls: &calls})
			if content == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				expectEqual(t, string(writer.written), `{"Text":"valid"}`)
			} else if !errors.Is(err, errInvalidText) {
				t.Fatalf("invalid text in fallback: %v", err)
			}
			expectEqual(t, calls, 1)
		})
	}
	for _, decline := range []bool{false, true} {
		var nativeCalls, calls int
		want, original := json.Marshal(_MalformedInputJSON{Text: "valid", decline: decline, calls: &nativeCalls})
		var writer = unboundedWriter()
		_, err := writer.admitted("input", _MalformedInputJSON{Text: "valid", decline: decline, calls: &calls})
		if original != nil {
			if err == nil {
				t.Fatal("input accepted metadata rejected by native JSONv2")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			expectEqual(t, string(writer.written), string(want))
		}
		expectEqual(t, calls, nativeCalls)
	}

}

func TestInputCapStopsTheNativeStreamWithinAValue(t *testing.T) {
	var before, after int
	var value = struct {
		First _CountingMarshaler
		Large string
		Last  _CountingMarshaler
	}{_CountingMarshaler{calls: &before}, strings.Repeat("x", 32<<10), _CountingMarshaler{calls: &after}}
	_, err := writeWork(_Ask{args: []any{value}}, 128)
	expectError(t, err, InvalidInput, "while writing argument 1")
	expectEqual(t, before, 1)
	expectEqual(t, after, 0)
}
