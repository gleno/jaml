package jaml

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gleno/fault"
	"go.yaml.in/yaml/v3"
)

const maxAliasedValues = 1 << 20

var (
	integerSyntax = regexp.MustCompile(`^[-+]?(0x[0-9a-fA-F_]+|0o[0-7_]+|0b[01_]+|[0-9][0-9_]*)$`)
	legacyOctal   = regexp.MustCompile(`^[-+]?0[0-9]+$`)
	floatSyntax   = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9][0-9_]*(\.[0-9_]*)?)([eE][-+]?[0-9]+)?$`)
)

func parseYAML(source string) (any, error) {
	document, err := readOneDocument(source)
	if err != nil {
		return nil, err
	}
	if document.Kind == 0 || len(document.Content) == 0 {
		return nil, nil
	}
	reader := &yamlReader{expanding: map[*yaml.Node]bool{}}
	return reader.data(document.Content[0], dataAt{})
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
	if len(document.Content) == 0 {
		return true
	}
	content := document.Content[0]
	return content.Kind == yaml.ScalarNode && content.Style == 0 && content.Value == ""
}

type dataAt struct {
	path    *dataPath
	aliased bool
}

func (a dataAt) atKey(key string) dataAt {
	return dataAt{path: a.path.atKey(key), aliased: a.aliased}
}

func (a dataAt) atIndex(index int) dataAt {
	return dataAt{path: a.path.atIndex(index), aliased: a.aliased}
}

type yamlReader struct {
	expanding     map[*yaml.Node]bool
	aliasedValues int
}

func (r *yamlReader) data(node *yaml.Node, at dataAt) (any, error) {
	if node.Kind == yaml.AliasNode {
		return r.alias(node, at)
	}
	if at.aliased {
		r.aliasedValues++
		if r.aliasedValues > maxAliasedValues {
			return nil, refuse(at.path, fmt.Sprintf("aliases expand to more than %d values", maxAliasedValues))
		}
	}
	if node.Kind == yaml.ScalarNode {
		return resolveScalar(node, at.path)
	}
	if at.path.depth() >= maxDataDepth {
		return nil, fault.From(InvalidTemplate, tooDeep(at.path.String()).Error())
	}
	if node.Anchor != "" {
		r.expanding[node] = true
		defer delete(r.expanding, node)
	}
	switch node.Kind {
	case yaml.MappingNode:
		return r.mapping(node, at)
	case yaml.SequenceNode:
		return r.sequence(node, at)
	}
	return nil, refuse(at.path, fmt.Sprintf("line %d holds a YAML node of unknown kind %d", node.Line, node.Kind))
}

func (r *yamlReader) alias(node *yaml.Node, at dataAt) (any, error) {
	if r.expanding[node.Alias] {
		return nil, refuse(at.path, fmt.Sprintf("alias *%s refers to a node that contains it", node.Value))
	}
	at.aliased = true
	return r.data(node.Alias, at)
}

func (r *yamlReader) sequence(node *yaml.Node, at dataAt) ([]any, error) {
	data := make([]any, len(node.Content))
	for index, item := range node.Content {
		converted, err := r.data(item, at.atIndex(index))
		if err != nil {
			return nil, err
		}
		data[index] = converted
	}
	return data, nil
}

func (r *yamlReader) mapping(node *yaml.Node, at dataAt) (map[string]any, error) {
	data := make(map[string]any, len(node.Content)/2)
	lines := make(map[string]int, len(node.Content)/2)
	var merged []map[string]any
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valueNode := node.Content[i], node.Content[i+1]
		key, err := mappingKey(keyNode, at.path)
		if err != nil {
			return nil, err
		}
		if first, seen := lines[key]; seen {
			return nil, refuse(at.path, fmt.Sprintf("line %d: mapping key %q already defined at line %d", keyNode.Line, key, first))
		}
		lines[key] = keyNode.Line
		if isMergeKey(keyNode) {
			sources, err := r.mergeSources(valueNode, at)
			if err != nil {
				return nil, err
			}
			merged = append(merged, sources...)
			continue
		}
		value, err := r.data(valueNode, at.atKey(key))
		if err != nil {
			return nil, err
		}
		data[key] = value
	}
	for _, source := range merged {
		for key, value := range source {
			if _, taken := data[key]; !taken {
				data[key] = value
			}
		}
	}
	return data, nil
}

func (r *yamlReader) mergeSources(node *yaml.Node, at dataAt) ([]map[string]any, error) {
	items := []*yaml.Node{node}
	if node.Kind == yaml.SequenceNode {
		items = node.Content
	}
	sources := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if dealias(item).Kind != yaml.MappingNode {
			return nil, refuse(at.path, fmt.Sprintf("line %d: a merge key (<<) takes a mapping or a list of mappings", item.Line))
		}
		source, err := r.data(item, at)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source.(map[string]any))
	}
	return sources, nil
}

func dealias(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

func isMergeKey(node *yaml.Node) bool {
	if node.Kind != yaml.ScalarNode || node.Value != "<<" {
		return false
	}
	return node.Style == 0 || node.Style == yaml.TaggedStyle && node.Tag == "!!merge"
}

func mappingKey(node *yaml.Node, at *dataPath) (string, error) {
	if isMergeKey(node) {
		return node.Value, nil
	}
	target := dealias(node)
	if target.Kind != yaml.ScalarNode {
		return "", refuse(at, fmt.Sprintf("the key on line %d is a collection; keys must be strings", node.Line))
	}
	resolved, err := resolveScalar(target, at)
	if err != nil {
		return "", err
	}
	name, isString := resolved.(string)
	if !isString {
		return "", refuse(at, fmt.Sprintf("key %s is %s; keys must be strings, so quote it", target.Value, describeScalar(resolved)))
	}
	return name, nil
}

func describeScalar(value any) string {
	switch value.(type) {
	case nil:
		return "a null"
	case bool:
		return "a bool"
	default:
		return "a number"
	}
}

func resolveScalar(node *yaml.Node, at *dataPath) (any, error) {
	value, err := scalarValue(node)
	if err != nil {
		return nil, refuse(at, err.Error())
	}
	if text, isText := value.(string); isText && !utf8.ValidString(text) {
		return nil, refuse(at, fmt.Sprintf("%s %s does not hold UTF-8 text", node.Tag, preview(node.Value)))
	}
	return value, nil
}

func scalarValue(node *yaml.Node) (any, error) {
	switch {
	case node.Style&yaml.TaggedStyle != 0:
		return taggedScalar(node.Tag, node.Value)
	case node.Style != 0:
		return node.Value, nil
	default:
		return plainScalar(node.Value)
	}
}

func plainScalar(written string) (any, error) {
	if value, known := spelledScalar(written); known {
		return value, nil
	}
	if isNonFinite(written) {
		return nil, notFinite(written)
	}
	if integerSyntax.MatchString(written) {
		digits := strings.ReplaceAll(written, "_", "")
		if legacyOctal.MatchString(digits) {
			return written, nil
		}
		if number, err := parseInteger(written); err == nil {
			return number, nil
		}
		return written, nil
	}
	if floatSyntax.MatchString(written) {
		return finiteFloat(written)
	}
	return written, nil
}

func taggedScalar(tag string, written string) (any, error) {
	switch tag {
	case "!!binary":
		decoded, err := base64.StdEncoding.DecodeString(written)
		if err != nil {
			return nil, fmt.Errorf("!!binary %s is not base64: %w", preview(written), err)
		}
		return string(decoded), nil
	case "!!int":
		if !integerSyntax.MatchString(written) {
			return nil, fmt.Errorf("!!int %s is not an integer", preview(written))
		}
		return parseInteger(written)
	case "!!float":
		if isNonFinite(written) {
			return nil, notFinite(written)
		}
		if !floatSyntax.MatchString(written) {
			return nil, fmt.Errorf("!!float %s is not a number", preview(written))
		}
		return finiteFloat(written)
	case "!!bool", "!!null":
		value, known := spelledScalar(written)
		if !known || tag == "!!bool" && value == nil || tag == "!!null" && value != nil {
			return nil, fmt.Errorf("%s %s is not one of its spellings", tag, preview(written))
		}
		return value, nil
	default:
		return written, nil
	}
}

func spelledScalar(written string) (any, bool) {
	switch written {
	case "", "~", "null", "Null", "NULL":
		return nil, true
	case "true", "True", "TRUE":
		return true, true
	case "false", "False", "FALSE":
		return false, true
	}
	return nil, false
}

func isNonFinite(written string) bool {
	switch written {
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF", ".nan", ".NaN", ".NAN":
		return true
	}
	return false
}

func notFinite(written string) error {
	return fmt.Errorf("%s is not a finite number, which JSON cannot carry", written)
}

func parseInteger(written string) (any, error) {
	number, err := strconv.ParseInt(strings.ReplaceAll(written, "_", ""), 0, 64)
	switch {
	case err == nil:
		return number, nil
	case errors.Is(err, strconv.ErrRange):
		return written, nil
	default:
		return nil, fmt.Errorf("%s is not an integer", preview(written))
	}
}

func finiteFloat(written string) (any, error) {
	number, err := strconv.ParseFloat(strings.ReplaceAll(written, "_", ""), 64)
	if errors.Is(err, strconv.ErrRange) && math.IsInf(number, 0) {
		return nil, fmt.Errorf("%s is out of the range of a 64-bit float; quote it to keep it as text", written)
	}
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, fmt.Errorf("%s is not a number", written)
	}
	return number, nil
}

func refuse(at *dataPath, message string) error {
	return fault.From(InvalidTemplate, at.String()+": "+message)
}
