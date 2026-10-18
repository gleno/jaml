package jaml

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/gleno/fault"
	"go.yaml.in/yaml/v3"
)

func parseYAML(source string) (any, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	if document.Kind == 0 {
		return nil, nil
	}
	keepTimestampsAsText(&document)
	var root any
	if err := document.Decode(&root); err != nil {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	return asJSONData(root, "$")
}

func keepTimestampsAsText(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!timestamp" {
		node.Tag = "!!str"
	}
	for _, child := range node.Content {
		keepTimestampsAsText(child)
	}
}

func asJSONData(node any, at string) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		data := make(map[string]any, len(typed))
		for key, item := range typed {
			converted, err := asJSONData(item, at+keyStep(key))
			if err != nil {
				return nil, err
			}
			data[key] = converted
		}
		return data, nil
	case map[any]any:
		data := make(map[string]any, len(typed))
		for key, item := range typed {
			name, isString := key.(string)
			if !isString {
				return nil, fault.From(InvalidTemplate, fmt.Sprintf("%s: key %v is a %T; keys must be strings, so quote it", at, key, key))
			}
			converted, err := asJSONData(item, at+keyStep(name))
			if err != nil {
				return nil, err
			}
			data[name] = converted
		}
		return data, nil
	case []any:
		data := make([]any, len(typed))
		for index, item := range typed {
			converted, err := asJSONData(item, at+indexStep(index))
			if err != nil {
				return nil, err
			}
			data[index] = converted
		}
		return data, nil
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return nil, fault.From(InvalidTemplate, fmt.Sprintf("%s: %v is not a finite number, which JSON cannot carry", at, typed))
		}
		return typed, nil
	default:
		return typed, nil
	}
}

func (t *template) selectPath(path []string) (any, string, error) {
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
