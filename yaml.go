package jaml

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	maxAliasedValues     = 1 << 20
	aliasRatioFloor      = 400_000
	aliasRatioCeiling    = 4_000_000
	aliasGuardMinAliased = 100
	aliasGuardMinDecoded = 1000
	generousAliasRatio   = 0.99
	strictAliasRatio     = 0.10
	hexDigits            = "0123456789abcdefABCDEF"
	decimalDigits        = "0123456789"
)

var (
	integerShape = regexp.MustCompile(`^[-+]?(0x[0-9a-fA-F]+|0o[0-7]+|0b[01]+|[0-9]+)$`)
	decimalShape = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	legacyOctal  = regexp.MustCompile(`^[-+]?0[0-9]+$`)
)

func parseYAML(ctx context.Context, source string) (any, error) {
	document, err := readOneDocument(source)
	if err != nil {
		return nil, err
	}
	if document.Kind == 0 || len(document.Content) == 0 {
		return nil, nil
	}
	return convertDocument(ctx, document.Content[0])
}

func convertDocument(ctx context.Context, root *yaml.Node) (any, error) {
	var reader = &_YamlReader{ctx: ctx, expanding: map[*yaml.Node]bool{}, converted: map[*yaml.Node]_Converted{}, deepest: -1}
	return reader.data(root, _DataPlace{})
}

func readOneDocument(source string) (*yaml.Node, error) {
	var decoder = yaml.NewDecoder(strings.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil && !errors.Is(err, io.EOF) {
		return nil, failWith(invalidExpression, err)
	}
	for {
		var another yaml.Node
		var err = decoder.Decode(&another)
		if errors.Is(err, io.EOF) {
			return &document, nil
		}
		if err != nil {
			return nil, failWith(invalidExpression, err)
		}
		if another.Kind != 0 && !isEmptyDocument(&another) {
			return nil, fail(invalidExpression, "line %d starts a second YAML document; a jaml document is one YAML document", another.Line)
		}
	}
}

func isEmptyDocument(document *yaml.Node) bool {
	if len(document.Content) == 0 {
		return true
	}
	var content = document.Content[0]
	return content.Kind == yaml.ScalarNode && content.Style == 0 && content.Value == ""
}

type _DataPlace struct {
	path     *_DataPath
	aliased  bool
	retained bool
}

func (a _DataPlace) atKey(key string) _DataPlace {
	return _DataPlace{path: a.path.atKey(key), aliased: a.aliased, retained: a.retained}
}

func (a _DataPlace) atIndex(index int) _DataPlace {
	return _DataPlace{path: a.path.atIndex(index), aliased: a.aliased, retained: a.retained}
}

func (a _DataPlace) release(read []*yaml.Node) {
	if a.retained {
		return
	}
	clear(read)
}

type _YamlReader struct {
	ctx       context.Context
	expanding map[*yaml.Node]bool
	converted map[*yaml.Node]_Converted
	budget    _AliasBudget
	deepest   int
}

type _Converted struct {
	value  any
	values int
	height int
}

type _AliasBudget struct {
	decoded int
	aliased int
}

func (b *_AliasBudget) spend(aliased bool) error {
	*b = b.after(1, aliased)
	if !aliased {
		return nil
	}
	return b.excess()
}

func (b _AliasBudget) after(values int, aliased bool) _AliasBudget {
	b.decoded += values
	if aliased {
		b.aliased += values
	}
	return b
}

func (b _AliasBudget) excess() error {
	if b.aliased > maxAliasedValues {
		return fmt.Errorf("aliases expand to more than %d values", maxAliasedValues)
	}
	if b.aliased > aliasGuardMinAliased && b.decoded > aliasGuardMinDecoded && float64(b.aliased) > allowedAliasRatio(b.decoded)*float64(b.decoded) {
		return fmt.Errorf("aliases expand to %d of the %d values read so far, which is excessive aliasing", b.aliased, b.decoded)
	}
	return nil
}

func allowedAliasRatio(decoded int) float64 {
	switch {
	case decoded <= aliasRatioFloor:
		return generousAliasRatio
	case decoded >= aliasRatioCeiling:
		return strictAliasRatio
	}
	var progress = float64(decoded-aliasRatioFloor) / float64(aliasRatioCeiling-aliasRatioFloor)
	return generousAliasRatio - (generousAliasRatio-strictAliasRatio)*progress
}

func (r *_YamlReader) data(node *yaml.Node, place _DataPlace) (any, error) {
	if r.ctx.Err() != nil {
		return nil, cancelled(r.ctx)
	}
	switch {
	case node.Kind == yaml.AliasNode:
		return r.alias(node, place)
	case node.Anchor != "":
		return r.anchored(node, place)
	}
	return r.convert(node, place)
}

func (r *_YamlReader) alias(node *yaml.Node, place _DataPlace) (any, error) {
	if r.expanding[node.Alias] {
		return nil, refuse(place.path, fmt.Sprintf("alias *%s refers to a node that contains it", node.Value))
	}
	place.aliased = true
	return r.anchored(node.Alias, place)
}

func (r *_YamlReader) anchored(node *yaml.Node, place _DataPlace) (any, error) {
	place.retained = true
	known, seen := r.converted[node]
	switch {
	case !seen:
		return r.convertOnce(node, place)
	case r.reuse(known, place):
		return known.value, nil
	}
	return r.convert(node, place)
}

func (r *_YamlReader) convertOnce(node *yaml.Node, place _DataPlace) (any, error) {
	var before, outer = r.budget.decoded, r.deepest
	r.deepest = -1
	value, err := r.convert(node, place)
	var inner = r.deepest
	r.deepest = max(outer, inner)
	if err != nil {
		return nil, err
	}
	var height = -1
	if inner >= 0 {
		height = inner - place.path.depth()
	}
	r.converted[node] = _Converted{value: value, values: r.budget.decoded - before, height: height}
	return value, nil
}

func (r *_YamlReader) reuse(known _Converted, place _DataPlace) bool {
	var deepest = place.path.depth() + known.height
	if known.height >= 0 && deepest >= maxDataDepth {
		return false
	}
	var charged = r.budget.after(known.values, place.aliased)
	if place.aliased && charged.excess() != nil {
		return false
	}
	r.budget = charged
	if known.height >= 0 {
		r.deepest = max(r.deepest, deepest)
	}
	return true
}

func (r *_YamlReader) convert(node *yaml.Node, place _DataPlace) (any, error) {
	if err := r.budget.spend(place.aliased); err != nil {
		return nil, refuse(place.path, err.Error())
	}
	if node.Kind == yaml.ScalarNode {
		return resolveScalar(node, place.path)
	}
	if place.path.depth() >= maxDataDepth {
		return nil, fail(invalidExpression, "%s", nestsTooDeep).at(place.path.String())
	}
	r.deepest = max(r.deepest, place.path.depth())
	if node.Anchor != "" {
		r.expanding[node] = true
		defer delete(r.expanding, node)
	}
	switch node.Kind {
	case yaml.MappingNode:
		return r.mapping(node, place)
	case yaml.SequenceNode:
		return r.sequence(node, place)
	}
	return nil, refuse(place.path, fmt.Sprintf("line %d holds a YAML node of unknown kind %d", node.Line, node.Kind))
}

func (r *_YamlReader) sequence(node *yaml.Node, place _DataPlace) ([]any, error) {
	var data = make([]any, len(node.Content))
	for index, item := range node.Content {
		converted, err := r.data(item, place.atIndex(index))
		if err != nil {
			return nil, err
		}
		data[index] = converted
		place.release(node.Content[index : index+1])
	}
	return data, nil
}

type _Mapping struct {
	keys   []string
	values map[string]any
}

func (m *_Mapping) with(key string, value any) {
	if _, taken := m.values[key]; taken {
		return
	}
	m.keys = append(m.keys, key)
	m.values[key] = value
}

func (r *_YamlReader) mapping(node *yaml.Node, place _DataPlace) (*_Mapping, error) {
	var own = make(map[string]any, len(node.Content)/2)
	var lines = make(map[string]int, len(node.Content)/2)
	var written []any
	var mergedAt = 0
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valueNode := node.Content[i], node.Content[i+1]
		if isMergeKey(keyNode) {
			if mergedAt != 0 {
				return nil, refuse(place.path, fmt.Sprintf("line %d: a mapping takes one merge key (<<) and line %d already has it; merge several mappings with <<: [*a, *b]", keyNode.Line, mergedAt))
			}
			mergedAt = keyNode.Line
			sources, err := r.mergeSources(valueNode, place)
			if err != nil {
				return nil, err
			}
			written = append(written, sources)
			place.release(node.Content[i : i+2])
			continue
		}
		key, err := mappingKey(keyNode, place.path)
		if err != nil {
			return nil, err
		}
		if first, seen := lines[key]; seen {
			return nil, refuse(place.path, fmt.Sprintf("line %d: mapping key %q already defined at line %d", keyNode.Line, key, first))
		}
		lines[key] = keyNode.Line
		value, err := r.data(valueNode, place.atKey(key))
		if err != nil {
			return nil, err
		}
		own[key] = value
		written = append(written, key)
		place.release(node.Content[i : i+2])
	}
	var data = &_Mapping{values: make(map[string]any, len(own))}
	for _, entry := range written {
		switch typed := entry.(type) {
		case string:
			data.with(typed, own[typed])
		case []*_Mapping:
			for _, source := range typed {
				for _, key := range source.keys {
					if _, overridden := own[key]; !overridden {
						data.with(key, source.values[key])
					}
				}
			}
		}
	}
	return data, nil
}

func (r *_YamlReader) mergeSources(node *yaml.Node, place _DataPlace) ([]*_Mapping, error) {
	var items = []*yaml.Node{node}
	if node.Kind == yaml.SequenceNode {
		items = node.Content
	}
	var sources = make([]*_Mapping, 0, len(items))
	for _, item := range items {
		if dealias(item).Kind != yaml.MappingNode {
			return nil, refuse(place.path, fmt.Sprintf("line %d: a merge key (<<) takes a mapping or a list of mappings", item.Line))
		}
		source, err := r.data(item, place)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source.(*_Mapping))
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

func mappingKey(node *yaml.Node, path *_DataPath) (string, error) {
	var target = dealias(node)
	if target.Kind != yaml.ScalarNode {
		return "", refuse(path, fmt.Sprintf("the key on line %d is a collection; keys must be strings", node.Line))
	}
	resolved, err := resolveScalar(target, path)
	if err != nil {
		return "", err
	}
	name, isString := resolved.(string)
	switch {
	case resolved == nil:
		return "", refuse(path, fmt.Sprintf("the key on line %d is null; keys must be strings, so quote it", node.Line))
	case !isString:
		return "", refuse(path, fmt.Sprintf("key %s is %s; keys must be strings, so quote it", target.Value, describeScalar(resolved)))
	}
	return name, nil
}

func describeScalar(value any) string {
	switch value.(type) {
	case bool:
		return "a bool"
	default:
		return "a number"
	}
}

func resolveScalar(node *yaml.Node, path *_DataPath) (any, error) {
	value, err := scalarValue(node)
	if err != nil {
		return nil, refuse(path, err.Error())
	}
	if text, isText := value.(string); isText && !utf8.ValidString(text) {
		return nil, refuse(path, fmt.Sprintf("%s %s does not hold UTF-8 text", node.Tag, excerpt(node.Value, codeExcerptLength)))
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
	number, err := readNumeral(written)
	switch {
	case err != nil:
		return nil, err
	case number.isInteger() && legacyOctal.MatchString(number.digits):
		return written, nil
	case number.isInteger():
		return number.integer()
	case number.isDecimal():
		return number.float()
	}
	return written, nil
}

func taggedScalar(tag string, written string) (any, error) {
	switch tag {
	case "!!binary":
		decoded, err := base64.StdEncoding.DecodeString(written)
		if err != nil {
			return nil, fmt.Errorf("!!binary %s is not base64: %w", shown(written), err)
		}
		return string(decoded), nil
	case "!!int":
		number, err := readNumeral(written)
		if err != nil {
			return nil, err
		}
		if !number.isInteger() {
			return nil, fmt.Errorf("!!int %s is not an integer", shown(written))
		}
		return number.integer()
	case "!!float":
		if isNonFinite(written) {
			return nil, notFinite(written)
		}
		number, err := readNumeral(written)
		if err != nil {
			return nil, err
		}
		if !number.isDecimal() {
			return nil, fmt.Errorf("!!float %s is not a number", shown(written))
		}
		return number.float()
	case "!!bool", "!!null":
		value, known := spelledScalar(written)
		if !known || tag == "!!bool" && value == nil || tag == "!!null" && value != nil {
			return nil, fmt.Errorf("%s %s is not one of its spellings", tag, shown(written))
		}
		return value, nil
	default:
		return written, nil
	}
}

func shown(written string) string {
	if written == "" {
		return `""`
	}
	return excerpt(written, codeExcerptLength)
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

type _Numeral struct {
	written string
	digits  string
}

func readNumeral(written string) (_Numeral, error) {
	if !strings.Contains(written, "_") || strings.HasPrefix(strings.TrimLeft(written, "+-"), "_") {
		return _Numeral{written: written, digits: written}, nil
	}
	var number = _Numeral{written: written, digits: strings.ReplaceAll(written, "_", "")}
	if (number.isInteger() || number.isDecimal()) && !number.underscoresSeparateDigits() {
		return _Numeral{}, fmt.Errorf("%s has an underscore that does not sit between two digits", shown(written))
	}
	return number, nil
}

func (n _Numeral) isInteger() bool {
	return integerShape.MatchString(n.digits)
}

func (n _Numeral) isDecimal() bool {
	return decimalShape.MatchString(n.digits)
}

func (n _Numeral) underscoresSeparateDigits() bool {
	var digits = decimalDigits
	if strings.HasPrefix(strings.TrimLeft(n.digits, "+-"), "0x") {
		digits = hexDigits
	}
	var unsigned = strings.TrimLeft(n.written, "+-")
	var prefixEnd = -1
	if len(unsigned) > 2 && unsigned[0] == '0' && strings.IndexByte("xob", unsigned[1]) >= 0 {
		prefixEnd = len(n.written) - len(unsigned) + 1
	}
	var isDigit = func(index int) bool {
		return index >= 0 && index < len(n.written) && strings.IndexByte(digits, n.written[index]) >= 0
	}
	for index := range len(n.written) {
		if n.written[index] == '_' && (!isDigit(index-1) && index-1 != prefixEnd || !isDigit(index+1)) {
			return false
		}
	}
	return true
}

func (n _Numeral) integer() (any, error) {
	number, err := strconv.ParseInt(n.digits, 0, 64)
	switch {
	case err == nil:
		return number, nil
	case errors.Is(err, strconv.ErrRange):
		return n.written, nil
	default:
		return nil, fmt.Errorf("%s is not an integer", shown(n.written))
	}
}

func (n _Numeral) float() (any, error) {
	number, err := strconv.ParseFloat(n.digits, 64)
	if errors.Is(err, strconv.ErrRange) && math.IsInf(number, 0) {
		return nil, fmt.Errorf("%s is out of the range of a 64-bit float; quote it to keep it as text", shown(n.written))
	}
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, fmt.Errorf("%s is not a number", shown(n.written))
	}
	return number, nil
}

func refuse(path *_DataPath, message string) error {
	return fail(invalidExpression, "%s", message).at(path.String())
}
