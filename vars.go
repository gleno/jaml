package jaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/dop251/goja"
)

var replacementEscape = []byte(`ufffd`)

type encodedVars map[string]json.RawMessage

var engineGlobals = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	for _, name := range goja.New().GlobalObject().GetOwnPropertyNames() {
		names[name] = true
	}
	return names
})

func encodeVars(vars map[string]any) (encodedVars, error) {
	encoded := make(encodedVars, len(vars))
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		shown := clip(name, keyExcerptLength)
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("var %q is not named in valid UTF-8", shown)
		}
		if engineGlobals()[name] {
			return nil, fmt.Errorf("var %q collides with a JavaScript global", shown)
		}
		value, err := encodeDatum(fmt.Sprintf("var %q", shown), vars[name])
		if err != nil {
			return nil, err
		}
		encoded[name] = value
	}
	return encoded, nil
}

func encodeArguments(args []any) ([]json.RawMessage, error) {
	encoded := make([]json.RawMessage, len(args))
	for index, arg := range args {
		value, err := encodeDatum(fmt.Sprintf("argument %d", index+1), arg)
		if err != nil {
			return nil, err
		}
		encoded[index] = value
	}
	return encoded, nil
}

func encodeDatum(named string, datum any) (json.RawMessage, error) {
	value, err := json.Marshal(datum)
	if err != nil {
		return nil, fmt.Errorf("%s is not JSON data: %w", named, err)
	}
	deepest, replaced := measureJSON(value)
	if deepest > maxDataDepth {
		return nil, tooDeep(named)
	}
	if replaced {
		return nil, fmt.Errorf("%s holds text that is not valid UTF-8", named)
	}
	return value, nil
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
