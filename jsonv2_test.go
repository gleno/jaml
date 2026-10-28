package jaml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strconv"
	"testing"
)

func TestJSONv2InputsUseNativeDefaults(t *testing.T) {
	type _Input struct {
		False   bool `json:"false,omitempty"`
		Zero    int  `json:"zero,omitempty"`
		Omitted int  `json:"omitted,omitzero"`
		Slice   []int
		Map     map[string]int
		Bytes   [2]byte
	}
	var inputs = map[string]any{
		"struct":                _Input{Bytes: [2]byte{1, 2}},
		"nil slice":             []any(nil),
		"nil map":               map[string]any(nil),
		"nested nil containers": map[string]any{"slice": []any(nil), "map": map[string]any(nil)},
		"replacement character": jsontext.Value(`"\ufffd"`),
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var argument = mustExpression(t, sb, "(a) => a")
		var binding = mustExpression(t, sb, "input")
		for name, input := range inputs {
			t.Run(name, func(t *testing.T) {
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				var want any
				if err := json.Unmarshal(encoded, &want); err != nil {
					t.Fatal(err)
				}
				expectEqual(t, mustResolve[any](t, t.Context(), argument, input), want)
				expectEqual(t, mustResolve[any](t, WithValues(t.Context(), map[string]any{"input": input}), binding), want)
			})
		}
		for _, raw := range []jsontext.Value{jsontext.Value(`{"x":1,"x":2}`), jsontext.Value(`{"nested":{"x":1,"x":2}}`)} {
			_, err := Resolve[any](t.Context(), argument, raw)
			expectError(t, err, InvalidInput, "duplicate")
			var syntax *jsontext.SyntacticError
			if !errors.As(err, &syntax) || !errors.Is(err, jsontext.ErrDuplicateName) {
				t.Fatal(err)
			}
		}
	})
}

func TestJSONv2LazyNamesMatchExplicitCasePolicy(t *testing.T) {
	type _Target struct {
		Exact   *Expression `json:"exact"`
		Ignored *Expression `json:"user_name,case:ignore"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var doc = mustJaml(t, sb, "EXACT: 1\nUSER-NAME: '${() => 7}'\n")
		var got = mustResolve[_Target](t, t.Context(), doc)
		expectEqual(t, got.Exact, (*Expression)(nil))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Ignored), 7)
		_, err := Resolve[_Target](t.Context(), mustJaml(t, sb, "user_name: '${1}'\nUSER-NAME: '${2}'\n"))
		expectError(t, err, CoercionFailed, "duplicate")
	})
}

type _JSONv2Key int

var jsonv2KeyCalls int

func (key *_JSONv2Key) UnmarshalJSON(written []byte) error {
	jsonv2KeyCalls++
	var text string
	if err := json.Unmarshal(written, &text); err != nil {
		return err
	}
	n, err := strconv.Atoi(text)
	*key = _JSONv2Key(n)
	return err
}

func TestJSONv2LazyMapKeysUseJSONMethodsOnce(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		jsonv2KeyCalls = 0
		var got = mustResolve[map[_JSONv2Key]*Expression](t, t.Context(), mustJaml(t, sb, "'1': '${11}'\n'2': '${22}'\n"))
		expectEqual(t, jsonv2KeyCalls, 2)
		expectEqual(t, mustResolve[int](t, t.Context(), got[1]), 11)
		expectEqual(t, mustResolve[int](t, t.Context(), got[2]), 22)
		var floats = mustResolve[map[float64]*Expression](t, t.Context(), mustJaml(t, sb, "'1.5': '${3}'\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), floats[1.5]), 3)
	})
}

func TestJSONv2ArrayLengthsAndErrorPointers(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, source := range []string{"[]", "[1]", "[1,2,3]"} {
			_, err := Resolve[[2]int](t.Context(), mustJaml(t, sb, source))
			expectError(t, err, CoercionFailed)
			var semantic *json.SemanticError
			if !errors.As(err, &semantic) {
				t.Fatal(err)
			}
		}
		_, err := Resolve[map[string][]int](t.Context(), mustJaml(t, sb, "'0': [1, bad]\n"))
		expectError(t, err, CoercionFailed, `$["0"][1]:`)
		var semantic *json.SemanticError
		if !errors.As(err, &semantic) {
			t.Fatal(err)
		}
		expectEqual(t, semantic.JSONPointer, jsontext.Pointer("/0/1"))
		_, err = Resolve[map[string][]int](t.Context(), mustJaml(t, sb, "'a/b~c': [bad]\n"))
		expectError(t, err, CoercionFailed, `$["a/b~c"][0]:`)
		if !errors.As(err, &semantic) {
			t.Fatal(err)
		}
		expectEqual(t, semantic.JSONPointer, jsontext.Pointer("/a~1b~0c/0"))
	})
}
