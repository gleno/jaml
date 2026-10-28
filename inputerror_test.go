package jaml

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

type _InputErrorSource struct {
	first error
	calls *int
}

func (v _InputErrorSource) failure() error {
	*v.calls++
	return v.first
}

type _InputErrorJSON _InputErrorSource
type _InputErrorJSONTo _InputErrorSource
type _InputErrorAppend _InputErrorSource

func (v _InputErrorJSON) MarshalJSON() ([]byte, error) {
	return nil, _InputErrorSource(v).failure()
}

func (v _InputErrorJSONTo) MarshalJSONTo(*jsontext.Encoder) error {
	return _InputErrorSource(v).failure()
}

func (v _InputErrorAppend) AppendText([]byte) ([]byte, error) {
	return nil, _InputErrorSource(v).failure()
}

func TestInputSyntaxErrorsFromCallbacksRetainTheirCause(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"UTF-8":     {'"', 0xff, '"'},
		"surrogate": []byte(`"\ud800"`),
	} {
		t.Run(name, func(t *testing.T) {
			_, original := jsonv2.Marshal(raw, json.DefaultOptionsV1(), jsontext.AllowInvalidUTF8(false))
			var syntax *json.SyntaxError
			if !errors.As(original, &syntax) {
				t.Fatalf("strict native encoding did not provide the required syntax error: %v", original)
			}
			for shape, first := range map[string]error{"native chain": original, "syntax leaf": syntax} {
				t.Run(shape, func(t *testing.T) {
					for method, makeValue := range map[string]func(*int) any{
						"MarshalText": func(calls *int) any { return _InputText{calls: calls, first: first} },
						"MarshalJSON": func(calls *int) any { return _InputErrorJSON{calls: calls, first: first} },
						"MarshalJSONTo": func(calls *int) any {
							return _InputErrorJSONTo{calls: calls, first: first}
						},
						"AppendText": func(calls *int) any { return _InputErrorAppend{calls: calls, first: first} },
					} {
						t.Run(method, func(t *testing.T) {
							var nativeCalls, inputCalls int
							_, native := wireJSON(makeValue(&nativeCalls))
							if !errors.Is(native, first) {
								t.Fatalf("native codec lost the callback error: %v", native)
							}
							_, err := writeWork(_Ask{args: []any{makeValue(&inputCalls)}}, unboundedAsk)
							expectEqual(t, nativeCalls, 1)
							expectEqual(t, inputCalls, nativeCalls)
							expectError(t, err, InvalidInput, "argument 1 holds text that is not valid UTF-8")
							if !errors.Is(err, first) {
								t.Fatalf("input classification lost the callback error: %v", err)
							}
							var got *json.SyntaxError
							if !errors.As(err, &got) || got != syntax {
								t.Fatalf("input classification lost native syntax metadata: got %v, want %v", got, syntax)
							}
						})
					}
				})
			}
		})
	}
}
