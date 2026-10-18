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
		encoded[name] = value
	}
	return encoded, nil
}

type inProcess struct{}

func (inProcess) open(vars encodedVars) (runtime, error) {
	return openSession(vars)
}
