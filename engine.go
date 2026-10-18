package jaml

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

type encodedVars map[string]json.RawMessage

type engine interface {
	open(vars encodedVars) (runtime, error)
}

type runtime interface {
	evaluate(compiled expression) (any, error)
	close()
}

func encodeVars(vars map[string]any) (encodedVars, error) {
	encoded := make(encodedVars, len(vars))
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		value, err := json.Marshal(vars[name])
		if err != nil {
			return nil, fmt.Errorf("var %q is not JSON data: %w", name, err)
		}
		if jsonDepth(value) > maxDataDepth {
			return nil, tooDeep(fmt.Sprintf("var %q", name))
		}
		encoded[name] = value
	}
	return encoded, nil
}

func jsonDepth(encoded []byte) int {
	depth, deepest, inString, escaped := 0, 0, false, false
	for _, b := range encoded {
		switch {
		case escaped:
			escaped = false
		case inString:
			escaped = b == '\\'
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
	return deepest
}

type inProcess struct{}

func (inProcess) open(vars encodedVars) (runtime, error) {
	return openSession(vars)
}
