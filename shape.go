package jaml

import (
	"encoding"
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type _Shape struct {
	Nodes []_ShapeNode `json:"nodes"`
}

type _ShapeNode struct {
	Expression bool          `json:"expression,omitzero"`
	Fields     []_ShapeField `json:"fields,omitempty"`
	Items      int           `json:"items,omitzero"`
	Fallback   int           `json:"fallback,omitzero"`
}

type _ShapeField struct {
	Name   string `json:"name"`
	Node   int    `json:"node"`
	Strict bool   `json:"strict,omitzero"`
}

type _ShapeCursor struct {
	shape *_Shape
	node  int
}

var (
	expressionType      = reflect.TypeFor[Expression]()
	expressionPointer   = reflect.TypeFor[*Expression]()
	unmarshalerType     = reflect.TypeFor[json.Unmarshaler]()
	unmarshalerFromType = reflect.TypeFor[json.UnmarshalerFrom]()
	textType            = reflect.TypeFor[encoding.TextUnmarshaler]()
	shapes              sync.Map
)

type _DrawnShape struct {
	nodes []_ShapeNode
	err   error
}

func shapeOf(target reflect.Type) ([]_ShapeNode, error) {
	if known, found := shapes.Load(target); found {
		return known.(_DrawnShape).nodes, known.(_DrawnShape).err
	}
	var drawing = &_ShapeDrawing{root: target, nodes: map[reflect.Type]int{}}
	var drawn = _DrawnShape{nodes: drawing.draw()}
	if drawing.refusal != nil {
		drawn = _DrawnShape{err: drawing.refusal}
	}
	shapes.Store(target, drawn)
	return drawn.nodes, drawn.err
}

type _ShapeDrawing struct {
	root    reflect.Type
	nodes   map[reflect.Type]int
	drawn   []_ShapeNode
	refusal error
}

func (d *_ShapeDrawing) draw() []_ShapeNode {
	var root = d.node(d.root)
	if root < 0 {
		return nil
	}
	var reaching = d.reaching()
	if !reaching[root] {
		return nil
	}
	return d.pruned(root, reaching)
}

func (d *_ShapeDrawing) node(target reflect.Type) int {
	if target == expressionPointer {
		if index, seen := d.nodes[target]; seen {
			return index
		}
		return d.add(target, _ShapeNode{Expression: true})
	}
	var pointers = map[reflect.Type]bool{}
	for target.Kind() == reflect.Pointer {
		if pointers[target] {
			d.refuse(d.cannotDecode(target, "a recursive pointer type "+target.String()))
			return -1
		}
		pointers[target] = true
		target = target.Elem()
	}
	if index, seen := d.nodes[target]; seen {
		return index
	}
	if target == expressionType {
		d.refuse(fail(invalidTarget, "Resolve fills *jaml.Expression, never a jaml.Expression held by value, as %s holds", d.root).at(rootPlace))
		return -1
	}
	if decodesItself(target) {
		return -1
	}
	if reason := undecodable(target); reason != "" {
		d.refuse(d.cannotDecode(target, reason))
		return -1
	}
	switch target.Kind() {
	case reflect.Struct:
		// Decode an empty object to validate native struct metadata without
		// visiting field values. Custom decoders were made opaque above.
		if err := json.Unmarshal([]byte("{}"), reflect.New(target).Interface()); err != nil {
			d.refuse(failWith(invalidTarget, err).at(rootPlace))
			return -1
		}
		var index = d.add(target, _ShapeNode{})
		var fields []_ShapeField
		var layout = jsonLayout(target)
		for _, ordered := range layout.lookup {
			var field = layout.fields[ordered]
			fields = append(fields, _ShapeField{Name: field.name, Node: d.node(field.kind), Strict: field.strict})
		}
		d.drawn[index].Fields = fields
		if layout.fallback != nil {
			var kind = embeddedType(layout.fallback.kind)
			if kind.Kind() == reflect.Map {
				d.drawn[index].Fallback = d.node(kind.Elem()) + 1
			}
		}
		return index
	case reflect.Map, reflect.Slice, reflect.Array:
		var index = d.add(target, _ShapeNode{})
		d.drawn[index].Items = d.node(target.Elem()) + 1
		return index
	}
	return -1
}

func (d *_ShapeDrawing) refuse(refusal error) {
	if d.refusal == nil {
		d.refusal = refusal
	}
}

func (d *_ShapeDrawing) cannotDecode(target reflect.Type, reason string) error {
	if target == d.root {
		return fail(invalidTarget, "Resolve cannot decode into %s", reason).at(rootPlace)
	}
	return fail(invalidTarget, "Resolve cannot decode into %s, as %s holds", reason, d.root).at(rootPlace)
}

func undecodable(target reflect.Type) string {
	switch target.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return "a " + target.String()
	case reflect.Interface:
		if target.NumMethod() > 0 {
			return "the interface " + target.String()
		}
	case reflect.Map:
		if !decodableKey(target.Key()) {
			return target.String() + ", whose keys are " + target.Key().String()
		}
	}
	return ""
}

func decodableKey(key reflect.Type) bool {
	var seen = map[reflect.Type]bool{}
	for key.Kind() == reflect.Pointer && !decodesItself(key) {
		if seen[key] {
			return false
		}
		seen[key] = true
		key = key.Elem()
	}
	switch key.Kind() {
	case reflect.String, reflect.Float32, reflect.Float64, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	if decodesItself(key) {
		return true
	}
	return key.Kind() == reflect.Interface && key.NumMethod() == 0 || key.Kind() == reflect.Array && key.Elem().Kind() == reflect.Uint8
}

func decodesItself(target reflect.Type) bool {
	var pointer = reflect.PointerTo(target)
	return pointer.Implements(unmarshalerType) || pointer.Implements(unmarshalerFromType) || pointer.Implements(textType)
}

func (d *_ShapeDrawing) add(target reflect.Type, node _ShapeNode) int {
	d.nodes[target] = len(d.drawn)
	d.drawn = append(d.drawn, node)
	return len(d.drawn) - 1
}

func (d *_ShapeDrawing) reaching() []bool {
	var reaching = make([]bool, len(d.drawn))
	for changed := true; changed; {
		changed = false
		for index, node := range d.drawn {
			if reaching[index] {
				continue
			}
			var reaches = node.Expression || node.Items > 0 && reaching[node.Items-1] || node.Fallback > 0 && reaching[node.Fallback-1] || slices.ContainsFunc(node.Fields, func(field _ShapeField) bool { return field.Node >= 0 && reaching[field.Node] })
			if reaches {
				reaching[index], changed = true, true
			}
		}
	}
	return reaching
}

func (d *_ShapeDrawing) pruned(root int, reaching []bool) []_ShapeNode {
	var renumbered = map[int]int{root: 0}
	var order = []int{root}
	var number = func(index int) int {
		if known, seen := renumbered[index]; seen {
			return known
		}
		renumbered[index] = len(order)
		order = append(order, index)
		return renumbered[index]
	}
	var nodes []_ShapeNode
	for next := 0; next < len(order); next++ {
		var node = d.drawn[order[next]]
		var kept = _ShapeNode{Expression: node.Expression}
		for _, field := range node.Fields {
			var child = -1
			if field.Node >= 0 && reaching[field.Node] {
				child = number(field.Node)
			}
			kept.Fields = append(kept.Fields, _ShapeField{Name: field.Name, Node: child, Strict: field.Strict})
		}
		if node.Items > 0 && reaching[node.Items-1] {
			kept.Items = number(node.Items-1) + 1
		}
		if node.Fallback > 0 && reaching[node.Fallback-1] {
			kept.Fallback = number(node.Fallback-1) + 1
		}
		nodes = append(nodes, kept)
	}
	return nodes
}

func matchKey(count int, name func(int) string, strict func(int) bool, key string) int {
	for i := range count {
		if name(i) == key {
			return i
		}
	}
	var folded = foldName(key)
	var matched = -1
	for i := range count {
		if !strict(i) && foldName(name(i)) == folded {
			if matched >= 0 {
				return -2 // native JSONv2 rejects ambiguous case-insensitive names
			}
			matched = i
		}
	}
	return matched
}

func foldName(name string) string {
	var folded strings.Builder
	for _, r := range name {
		switch {
		case r == '-' || r == '_':
			continue
		case r < utf8.RuneSelf && 'a' <= r && r <= 'z':
			folded.WriteRune(r - 'a' + 'A')
		case r < utf8.RuneSelf:
			folded.WriteRune(r)
		default:
			for {
				next := unicode.SimpleFold(r)
				if next <= r {
					r = next
					break
				}
				r = next
			}
			folded.WriteRune(r)
		}
	}
	return folded.String()
}

func (c *_ShapeCursor) current() *_ShapeNode {
	if c == nil {
		return nil
	}
	return &c.shape.Nodes[c.node]
}

func (c *_ShapeCursor) skips() bool {
	var node = c.current()
	return node != nil && node.Expression
}

func (c *_ShapeCursor) key(key string) *_ShapeCursor {
	var node = c.current()
	if node == nil {
		return nil
	}
	if node.Items > 0 {
		return &_ShapeCursor{shape: c.shape, node: node.Items - 1}
	}
	var chosen = matchKey(len(node.Fields), func(i int) string { return node.Fields[i].Name }, func(i int) bool { return node.Fields[i].Strict }, key)
	if chosen == -2 {
		return nil
	}
	if chosen < 0 {
		return c.fallback()
	}
	if node.Fields[chosen].Node < 0 {
		return nil
	}
	return &_ShapeCursor{shape: c.shape, node: node.Fields[chosen].Node}
}

func (c *_ShapeCursor) fallback() *_ShapeCursor {
	var node = c.current()
	if node == nil || node.Fallback == 0 {
		return nil
	}
	return &_ShapeCursor{shape: c.shape, node: node.Fallback - 1}
}

func (c *_ShapeCursor) item() *_ShapeCursor {
	var node = c.current()
	if node == nil || node.Items == 0 {
		return nil
	}
	return &_ShapeCursor{shape: c.shape, node: node.Items - 1}
}
