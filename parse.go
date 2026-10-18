package jaml

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
)

func (t *template) selectPath(path []string) (any, *dataPath, error) {
	current, at := t.root, (*dataPath)(nil)
	for _, step := range path {
		switch typed := current.(type) {
		case map[string]any:
			found, ok := typed[step]
			if !ok {
				return nil, nil, missingPath(at, fmt.Sprintf("has no key %q", step))
			}
			current, at = found, at.atKey(step)
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || strconv.Itoa(index) != step || index < 0 || index >= len(typed) {
				return nil, nil, missingPath(at, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, at = typed[index], at.atIndex(index)
		default:
			return nil, nil, missingPath(at, fmt.Sprintf("is %s, which has no key %q", t.describe(current), step))
		}
	}
	return current, at, nil
}

func (t *template) describe(node any) string {
	switch typed := node.(type) {
	case nil:
		return "a null"
	case string:
		if _, hasCode := t.texts[typed]; hasCode {
			return "an expression, unevaluated until Eval, so it"
		}
		return "a string"
	case bool:
		return "a bool"
	default:
		return "a number"
	}
}

func missingPath(at *dataPath, message string) error {
	return fail(MissingPath, "%s %s", at, message)
}

func eachChild(node any, at *dataPath, visit func(child any, at *dataPath) error) error {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			if err := visit(typed[key], at.atKey(key)); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := visit(item, at.atIndex(index)); err != nil {
				return err
			}
		}
	}
	return nil
}
