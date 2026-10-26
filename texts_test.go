package jaml

import (
	"reflect"
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

func TestATextWalkStopsAtAnActiveContainer(t *testing.T) {
	for name, cycle := range map[string]func(_CountingText) any{
		"map": func(text _CountingText) any {
			var value = _TextMap{"text": text}
			value["left"], value["right"] = value, value
			return value
		},
		"slice": func(text _CountingText) any {
			var value = _TextSlice{text, nil, nil}
			value[1], value[2] = value, value
			return value
		},
		"pointer": func(text _CountingText) any {
			var value = &_TextLinks{Text: text}
			value.Left, value.Right = value, value
			return value
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			var value = cycle(_CountingText{calls: &calls})
			var walk = _TextWalk{structs: map[reflect.Type]_KnownStruct{}, followed: map[_Followed]bool{}}
			// A short remaining depth bounds even the broken branching walk.
			if !walk.valid(reflect.ValueOf(value), maxDataDepth-4) {
				t.Fatal("cycle detection belongs to the JSON encoder, not text validation")
			}
			if calls != 1 {
				t.Fatalf("visited the text in one active container %d times, want once", calls)
			}
		})
	}
}

func TestATextWalkRevisitsSharedContainersAfterLeavingThem(t *testing.T) {
	for name, shared := range map[string]func(_CountingText) any{
		"map":     func(text _CountingText) any { return _TextMap{"text": text} },
		"slice":   func(text _CountingText) any { return _TextSlice{text} },
		"pointer": func(text _CountingText) any { return &_TextLinks{Text: text} },
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			var value = shared(_CountingText{calls: &calls})
			if !holdsValidText([]any{value, value}) {
				t.Fatal("shared acyclic data was refused")
			}
			expectEqual(t, calls, 2)
		})
	}
}

func TestATextWalkDistinguishesOverlappingSliceViews(t *testing.T) {
	var longer = _TextSlice{nil, "\xff"}
	var shorter = longer[:1]
	longer[0] = longer
	if holdsValidText(shorter) {
		t.Fatal("the longer view's invalid text was hidden by the shorter active view")
	}
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
