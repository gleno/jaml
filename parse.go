package jaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/gleno/fault"
	"sigs.k8s.io/yaml"
)

func parseYAML(source string) (any, error) {
	converted, err := yaml.YAMLToJSON([]byte(source))
	if err != nil {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(converted))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	return root, nil
}

func (t *Template) selectPath(path []string) (any, string, error) {
	current, at := t.root, "$"
	for _, step := range path {
		switch typed := current.(type) {
		case map[string]any:
			found, ok := typed[step]
			if !ok {
				return nil, "", missingPath(at, fmt.Sprintf("has no key %q", step))
			}
			current, at = found, at+keyStep(step)
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, "", missingPath(at, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, at = typed[index], at+indexStep(index)
		default:
			return nil, "", missingPath(at, fmt.Sprintf("is %s, which has no key %q", t.describe(current), step))
		}
	}
	return current, at, nil
}

func (t *Template) describe(node any) string {
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

func missingPath(at string, message string) error {
	return fault.From(MissingPath, at+" "+message)
}

func eachChild(node any, at string, visit func(child any, at string) error) error {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			if err := visit(typed[key], at+keyStep(key)); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := visit(item, at+indexStep(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func keyStep(key string) string {
	if isPlainKey(key) {
		return "." + key
	}
	return "[" + strconv.Quote(key) + "]"
}

func indexStep(index int) string {
	return "[" + strconv.Itoa(index) + "]"
}

func isPlainKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		letter := r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}
