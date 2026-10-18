package jaml

import (
	"fmt"
	"strconv"

	"github.com/gleno/fault"
)

func selectPath(root value, at location, path []string) (value, location, error) {
	current := root
	for _, step := range path {
		switch typed := current.(type) {
		case objectValue:
			found, ok := typed.lookup(step)
			if !ok {
				return nil, location{}, missingPath(at, fmt.Sprintf("has no key %q", step))
			}
			current, at = found.value, at.key(step, found.at)
		case listValue:
			index, err := strconv.Atoi(step)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, location{}, missingPath(at, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, at = typed[index].value, at.index(index, typed[index].at)
		case expressionValue, interpolationValue:
			return nil, location{}, missingPath(at, fmt.Sprintf("is an expression, so it has no key %q until it is evaluated", step))
		default:
			return nil, location{}, missingPath(at, fmt.Sprintf("is a %s, which has no key %q", current.kind(), step))
		}
	}
	return current, at, nil
}

func missingPath(at location, message string) error {
	return fault.From(MissingPath, fmt.Sprintf("%s %s", at, message))
}
