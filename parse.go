package jaml

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gleno/fault"
	"go.yaml.in/yaml/v3"
)

var (
	legacyOctal = regexp.MustCompile(`^[-+]?0[0-9]+$`)
	integerLike = regexp.MustCompile(`^[-+]?(0x[0-9a-fA-F_]+|0o[0-7_]+|0b[01_]+|[0-9][0-9_]*)$`)
	floatLike   = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9][0-9_]*(\.[0-9_]*)?)([eE][-+]?[0-9]+)?$`)
)

func parseYAML(source string) (any, error) {
	document, err := readOneDocument(source)
	if err != nil {
		return nil, err
	}
	if document.Kind == 0 {
		return nil, nil
	}
	if err := settleScalars(document, "$"); err != nil {
		return nil, err
	}
	var root any
	if err := document.Decode(&root); err != nil {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	return asJSONData(root, "$")
}

func readOneDocument(source string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil && !errors.Is(err, io.EOF) {
		return nil, fault.From(InvalidTemplate, err.Error())
	}
	for {
		var another yaml.Node
		err := decoder.Decode(&another)
		if errors.Is(err, io.EOF) {
			return &document, nil
		}
		if err != nil {
			return nil, fault.From(InvalidTemplate, err.Error())
		}
		if another.Kind != 0 && !isEmptyDocument(&another) {
			return nil, fault.From(InvalidTemplate, fmt.Sprintf("line %d starts a second YAML document; a template is one document", another.Line))
		}
	}
}

func isEmptyDocument(document *yaml.Node) bool {
	return document.Kind == yaml.DocumentNode && (len(document.Content) == 0 || document.Content[0].ShortTag() == "!!null" && document.Content[0].Value == "")
}

func settleScalars(node *yaml.Node, at string) error {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for index, child := range node.Content {
			childAt := at
			if node.Kind == yaml.SequenceNode {
				childAt = at + indexStep(index)
			}
			if err := settleScalars(child, childAt); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if err := settleScalars(key, at); err != nil {
				return err
			}
			if err := settleScalars(value, at+keyStep(key.Value)); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		return settleScalar(node, at)
	}
	return nil
}

func settleScalar(node *yaml.Node, at string) error {
	if node.ShortTag() == "!!timestamp" {
		node.Tag = "!!str"
		return nil
	}
	if node.Style != 0 {
		return nil
	}
	switch {
	case legacyOctal.MatchString(node.Value), integerLike.MatchString(node.Value) && outOfInt64(node.Value):
		node.Tag = "!!str"
	case floatLike.MatchString(node.Value) && outOfFloat64(node.Value):
		return fault.From(InvalidTemplate, fmt.Sprintf("%s: %s is out of the range of a 64-bit float; quote it to keep it as text", at, node.Value))
	}
	return nil
}

func outOfInt64(written string) bool {
	_, err := strconv.ParseInt(written, 0, 64)
	return errors.Is(err, strconv.ErrRange)
}

func outOfFloat64(written string) bool {
	number, err := strconv.ParseFloat(strings.ReplaceAll(written, "_", ""), 64)
	return errors.Is(err, strconv.ErrRange) && math.IsInf(number, 0)
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
