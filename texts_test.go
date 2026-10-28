package jaml

import (
	"testing"
)

type _CountingText struct{ calls *int }

func (c _CountingText) MarshalText() ([]byte, error) {
	*c.calls++
	return []byte("ok"), nil
}

type _TextMap map[string]any

type _TextSlice []any

type _TextLinks struct {
	Text  _CountingText
	Left  *_TextLinks
	Right *_TextLinks
}

func TestCyclicContainersAreRefusedAsValuesAndArguments(t *testing.T) {
	var object = _TextMap{}
	object["self"] = object
	var list = make(_TextSlice, 1)
	list[0] = list
	var calls int
	var pointer = &_TextLinks{Text: _CountingText{calls: &calls}}
	pointer.Left = pointer
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var compiled = mustExpression(t, sb, "(a) => typeof a")
		for name, value := range map[string]any{"map": object, "slice": list, "pointer": pointer} {
			t.Run(name, func(t *testing.T) {
				_, err := Resolve[string](withValues(t, map[string]any{"bad": value}), compiled)
				expectError(t, err, InvalidInput, `binding values: value "bad" is not JSON data`, "encountered a cycle")
				_, err = Resolve[string](t.Context(), compiled, value)
				expectError(t, err, InvalidInput, "binding arguments: argument 1 is not JSON data", "encountered a cycle")
			})
		}
	})
}
