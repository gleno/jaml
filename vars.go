package jaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

var replacementEscape = []byte(`ufffd`)

type encodedVars map[string]json.RawMessage

func encodeVars(vars map[string]any) (encodedVars, error) {
	encoded := make(encodedVars, len(vars))
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("var %q is not named in valid UTF-8", name)
		}
		value, err := json.Marshal(vars[name])
		if err != nil {
			return nil, fmt.Errorf("var %q is not JSON data: %w", name, err)
		}
		deepest, replaced := measureJSON(value)
		if deepest > maxDataDepth {
			return nil, tooDeep(fmt.Sprintf("var %q", name))
		}
		if replaced {
			return nil, fmt.Errorf("var %q holds text that is not valid UTF-8", name)
		}
		encoded[name] = value
	}
	return encoded, nil
}

func jsonDepth(encoded []byte) int {
	deepest, _ := measureJSON(encoded)
	return deepest
}

func measureJSON(encoded []byte) (deepest int, replaced bool) {
	depth, inString := 0, false
	for i := 0; i < len(encoded); i++ {
		switch b := encoded[i]; {
		case inString && b == '\\':
			replaced = replaced || bytes.HasPrefix(encoded[i+1:], replacementEscape)
			i++
		case inString:
			inString = b != '"'
		case b == '"':
			inString = true
		case b == '[' || b == '{':
			depth++
			deepest = max(deepest, depth)
		case b == ']' || b == '}':
			depth--
		}
	}
	return deepest, replaced
}
