package jaml

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
)

func blame(path string, encoded []byte, err error) string {
	// Only the outer error has coordinates in the complete decoded document.
	// A callback's wrapped errors may refer to a private input instead.
	var pointer jsontext.Pointer
	switch failure := err.(type) {
	case *json.SemanticError:
		pointer = failure.JSONPointer
	case *jsontext.SyntacticError:
		pointer = failure.JSONPointer
	default:
		return path
	}
	if pointer == "" {
		return path
	}
	// Use the original containers to distinguish a numeric object key from an
	// array index when rendering the native JSON pointer as a JAML location.
	var decoder = jsontext.NewDecoder(bytes.NewReader(encoded))
	var parents []*_DataPath
	var current *_DataPath
	for {
		kind, length := decoder.StackIndex(decoder.StackDepth())
		token, err := decoder.ReadToken()
		if err != nil {
			return path
		}
		if token.Kind() == '}' || token.Kind() == ']' {
			parents = parents[:len(parents)-1]
			continue
		}
		if len(parents) > 0 {
			var parent = parents[len(parents)-1]
			switch {
			case kind == '{' && length%2 == 0:
				current = parent.atKey(token.String())
			case kind == '[':
				current = parent.atIndex(int(length))
			}
		}
		if decoder.StackPointer() == pointer {
			return current.spelledFrom(path)
		}
		if token.Kind() == '{' || token.Kind() == '[' {
			parents = append(parents, current)
		}
	}
}
