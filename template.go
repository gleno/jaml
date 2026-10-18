package jaml

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/gleno/fault"
	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

const maxExpandedNodes = 1 << 20

var (
	plainNull    = regexp.MustCompile(`^(~|null|Null|NULL|)$`)
	plainTrue    = regexp.MustCompile(`^(true|True|TRUE)$`)
	plainFalse   = regexp.MustCompile(`^(false|False|FALSE)$`)
	plainDecimal = regexp.MustCompile(`^[-+]?[0-9]+$`)
	plainOctal   = regexp.MustCompile(`^0o[0-7]+$`)
	plainHex     = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	plainFloat   = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	plainInf     = regexp.MustCompile(`^[-+]?(\.inf|\.Inf|\.INF)$`)
	plainNaN     = regexp.MustCompile(`^(\.nan|\.NaN|\.NAN)$`)
)

type built struct {
	value value
	size  int
}

type builder struct {
	done     map[*yaml.Node]built
	building map[*yaml.Node]bool
}

func parseTemplate(text string) (value, position, error) {
	document, err := readTheOneDocument(text)
	if err != nil {
		return nil, position{}, fault.From(InvalidTemplate, err.Error())
	}
	if document == nil {
		return nullValue{}, position{}, nil
	}
	b := builder{done: map[*yaml.Node]built{}, building: map[*yaml.Node]bool{}}
	at := positionOf(document)
	root, err := b.build(document, rootLocation(at))
	if err != nil {
		return nil, position{}, err
	}
	return root.value, at, nil
}

func readTheOneDocument(text string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var another yaml.Node
	if err := decoder.Decode(&another); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("a template holds one YAML document, found more")
	}
	if len(document.Content) == 0 {
		return nil, nil
	}
	return document.Content[0], nil
}

func positionOf(node *yaml.Node) position {
	return position{line: node.Line, column: node.Column}
}

func (b builder) build(node *yaml.Node, at location) (built, error) {
	if node.Kind == yaml.AliasNode {
		return b.build(node.Alias, at)
	}
	if done, ok := b.done[node]; ok {
		return done, nil
	}
	if b.building[node] {
		return built{}, invalidAt(at, "an alias refers to a node that contains it")
	}
	b.building[node] = true
	result, err := b.buildFresh(node, at)
	delete(b.building, node)
	if err != nil {
		return built{}, err
	}
	if result.size > maxExpandedNodes {
		return built{}, invalidAt(at, fmt.Sprintf("aliases expand the template past %d nodes", maxExpandedNodes))
	}
	b.done[node] = result
	return result, nil
}

func (b builder) buildFresh(node *yaml.Node, at location) (built, error) {
	switch node.Kind {
	case yaml.MappingNode:
		if err := refuseExplicitTag(node, "!!map", at); err != nil {
			return built{}, err
		}
		return b.buildMapping(node, at)
	case yaml.SequenceNode:
		if err := refuseExplicitTag(node, "!!seq", at); err != nil {
			return built{}, err
		}
		return b.buildSequence(node, at)
	case yaml.ScalarNode:
		scalar, err := buildScalar(node, at)
		return built{value: scalar, size: 1}, err
	default:
		return built{}, invalidAt(at, "unsupported YAML node")
	}
}

func refuseExplicitTag(node *yaml.Node, allowed string, at location) error {
	if node.Style&yaml.TaggedStyle != 0 && node.ShortTag() != allowed {
		return invalidAt(at, fmt.Sprintf("unsupported tag %s", node.Tag))
	}
	return nil
}

func (b builder) buildMapping(node *yaml.Node, at location) (built, error) {
	object := make(objectValue, 0, len(node.Content)/2)
	size := 1
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, err := readKey(node.Content[i], at)
		if err != nil {
			return built{}, err
		}
		keyAt := at.key(key, positionOf(node.Content[i]))
		if seen[key] {
			return built{}, invalidAt(keyAt, "duplicate key")
		}
		seen[key] = true
		child, err := b.build(node.Content[i+1], at.key(key, positionOf(node.Content[i+1])))
		if err != nil {
			return built{}, err
		}
		size += child.size
		object = append(object, field{key: key, at: positionOf(node.Content[i+1]), value: child.value})
	}
	return built{value: object, size: size}, nil
}

func readKey(node *yaml.Node, at location) (string, error) {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	keyAt := at.descend("", positionOf(node))
	if node.Kind != yaml.ScalarNode {
		return "", invalidAt(keyAt, "mapping keys must be scalars")
	}
	if node.ShortTag() == "!!merge" {
		return "", invalidAt(keyAt, "merge keys (<<) are not supported")
	}
	return node.Value, nil
}

func (b builder) buildSequence(node *yaml.Node, at location) (built, error) {
	list := make(listValue, 0, len(node.Content))
	size := 1
	for i, item := range node.Content {
		child, err := b.build(item, at.index(i, positionOf(item)))
		if err != nil {
			return built{}, err
		}
		size += child.size
		list = append(list, element{at: positionOf(item), value: child.value})
	}
	return built{value: list, size: size}, nil
}

func buildScalar(node *yaml.Node, at location) (value, error) {
	if node.Style&yaml.TaggedStyle != 0 {
		return buildTaggedScalar(node, at)
	}
	if node.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return buildText(node.Value, at)
	}
	if resolved, isData := resolvePlain(node.Value); isData {
		return resolved, nil
	}
	return buildText(node.Value, at)
}

func buildTaggedScalar(node *yaml.Node, at location) (value, error) {
	text := node.Value
	tag := node.ShortTag()
	if tag == "!!str" {
		return stringValue(text), nil
	}
	resolved, isData := resolvePlain(text)
	if isData && tagOf(resolved) == tag {
		return resolved, nil
	}
	if tag == "!!float" {
		if number, isNumber := resolved.(numberValue); isNumber {
			return number, nil
		}
	}
	switch tag {
	case "!!null", "!!bool", "!!int", "!!float":
		return nil, invalidAt(at, fmt.Sprintf("%q is not a valid %s", text, tag))
	default:
		return nil, invalidAt(at, fmt.Sprintf("unsupported tag %s", node.Tag))
	}
}

func tagOf(resolved value) string {
	switch typed := resolved.(type) {
	case nullValue:
		return "!!null"
	case boolValue:
		return "!!bool"
	case numberValue:
		if plainDecimal.MatchString(typed.raw) || plainOctal.MatchString(typed.raw) || plainHex.MatchString(typed.raw) {
			return "!!int"
		}
		return "!!float"
	default:
		return ""
	}
}

func resolvePlain(text string) (value, bool) {
	switch {
	case plainNull.MatchString(text):
		return nullValue{}, true
	case plainTrue.MatchString(text):
		return boolValue{truth: true, raw: text}, true
	case plainFalse.MatchString(text):
		return boolValue{truth: false, raw: text}, true
	case plainDecimal.MatchString(text):
		return resolveDecimal(text), true
	case plainOctal.MatchString(text):
		return resolveInteger(text, text[2:], 8), true
	case plainHex.MatchString(text):
		return resolveInteger(text, text[2:], 16), true
	case plainFloat.MatchString(text):
		return resolveFloat(text), true
	case plainInf.MatchString(text), plainNaN.MatchString(text):
		return unrepresentable{raw: text, reason: text + " is not a finite number, which JSON cannot carry"}, true
	default:
		return nil, false
	}
}

func resolveDecimal(text string) value {
	digits := strings.TrimLeft(text, "+-")
	if len(digits) > 1 && digits[0] == '0' {
		return unrepresentable{raw: text, reason: text + " is ambiguous: YAML 1.1 reads it as octal, YAML 1.2 as decimal; write 0o" + strings.TrimLeft(digits, "0") + " or " + strings.TrimLeft(digits, "0") + ", or quote it"}
	}
	return resolveInteger(text, strings.TrimPrefix(text, "+"), 10)
}

func resolveInteger(raw string, digits string, base int) value {
	integer, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return unrepresentable{raw: raw, reason: raw + " is not an integer"}
	}
	return numberValue{text: integer.String(), raw: raw}
}

func resolveFloat(raw string) value {
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(number, 0) {
		return unrepresentable{raw: raw, reason: raw + " is out of the range of a 64-bit float"}
	}
	return numberValue{text: formatFloat(number), raw: raw}
}

func formatFloat(number float64) string {
	if number == 0 {
		return "0"
	}
	return strconv.FormatFloat(number, 'g', -1, 64)
}

func buildText(text string, at location) (value, error) {
	found, err := scan(text)
	if err != nil {
		return nil, invalidAt(at, err.Error())
	}
	if found.whole != "" {
		compiled, err := compileExpression(found.whole, false)
		if err != nil {
			return nil, invalidAt(at, fmt.Sprintf("${%s}: %s", preview(found.whole), err))
		}
		return expressionValue{expression: compiled}, nil
	}
	return buildInterpolation(text, found.segments, at)
}

func buildInterpolation(text string, scannedSegments []scannedSegment, at location) (value, error) {
	interpolated := make(interpolationValue, 0, len(scannedSegments))
	hasCode := false
	for _, scannedSegment := range scannedSegments {
		if !scannedSegment.code {
			interpolated = append(interpolated, segment{text: scannedSegment.text})
			continue
		}
		compiled, err := compileExpression(scannedSegment.text, true)
		if err != nil {
			return nil, invalidAt(at, fmt.Sprintf("${{%s}}: %s", preview(scannedSegment.text), err))
		}
		hasCode = true
		interpolated = append(interpolated, segment{expression: &compiled})
	}
	if !hasCode {
		return stringValue(text), nil
	}
	return interpolated, nil
}

func invalidAt(at location, message string) error {
	return fault.From(InvalidTemplate, fmt.Sprintf("%s: %s", at, message))
}
