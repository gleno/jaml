package jaml

import (
	"cmp"
	"encoding"
	"encoding/json"
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
	Expression bool          `json:"expression,omitempty"`
	Fields     []_ShapeField `json:"fields,omitempty"`
	Items      int           `json:"items,omitempty"`
}

type _ShapeField struct {
	Name string `json:"name"`
	Node int    `json:"node"`
}

type _ShapeCursor struct {
	shape *_Shape
	node  int
}

var (
	expressionType    = reflect.TypeFor[Expression]()
	expressionPointer = reflect.TypeFor[*Expression]()
	unmarshalerType   = reflect.TypeFor[json.Unmarshaler]()
	textType          = reflect.TypeFor[encoding.TextUnmarshaler]()
	shapes            sync.Map
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
	for target.Kind() == reflect.Pointer {
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
		var index = d.add(target, _ShapeNode{})
		var fields []_ShapeField
		for _, field := range jsonFields(target) {
			fields = append(fields, _ShapeField{Name: field.name, Node: d.node(field.kind)})
		}
		d.drawn[index].Fields = fields
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
	switch key.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return reflect.PointerTo(key).Implements(textType)
}

func decodesItself(target reflect.Type) bool {
	var pointer = reflect.PointerTo(target)
	return pointer.Implements(unmarshalerType) || pointer.Implements(textType)
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
			var reaches = node.Expression || node.Items > 0 && reaching[node.Items-1] || slices.ContainsFunc(node.Fields, func(field _ShapeField) bool { return field.Node >= 0 && reaching[field.Node] })
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
			kept.Fields = append(kept.Fields, _ShapeField{Name: field.Name, Node: child})
		}
		if node.Items > 0 && reaching[node.Items-1] {
			kept.Items = number(node.Items-1) + 1
		}
		nodes = append(nodes, kept)
	}
	return nodes
}

type _JSONField struct {
	name   string
	kind   reflect.Type
	tagged bool
	index  []int
}

func jsonFields(target reflect.Type) []_JSONField {
	var found []_JSONField
	var level = []_JSONField{{kind: target}}
	var counts map[reflect.Type]int
	var nextCounts = map[reflect.Type]int{}
	var visited = map[reflect.Type]bool{}
	for len(level) > 0 {
		var next []_JSONField
		counts, nextCounts = nextCounts, map[reflect.Type]int{}
		for _, embedding := range level {
			if visited[embedding.kind] {
				continue
			}
			visited[embedding.kind] = true
			for i := range embedding.kind.NumField() {
				promoted, field, isField := fieldOf(embedding, i)
				switch {
				case isField:
					found = append(found, field)
					if counts[embedding.kind] > 1 {
						found = append(found, field)
					}
				case promoted != nil:
					nextCounts[promoted.kind]++
					if nextCounts[promoted.kind] == 1 {
						next = append(next, *promoted)
					}
				}
			}
		}
		level = next
	}
	return dominantFields(found)
}

func fieldOf(embedding _JSONField, i int) (*_JSONField, _JSONField, bool) {
	var field = embedding.kind.Field(i)
	if field.Anonymous {
		var embedded = field.Type
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		if !field.IsExported() && embedded.Kind() != reflect.Struct {
			return nil, _JSONField{}, false
		}
	} else if !field.IsExported() {
		return nil, _JSONField{}, false
	}
	var tag = field.Tag.Get("json")
	if tag == "-" {
		return nil, _JSONField{}, false
	}
	var name, _, _ = strings.Cut(tag, ",")
	if !validTagName(name) {
		name = ""
	}
	var index = append(slices.Clip(embedding.index), i)
	var kind = field.Type
	if kind.Name() == "" && kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if name == "" && field.Anonymous && kind.Kind() == reflect.Struct {
		return &_JSONField{kind: kind, index: index}, _JSONField{}, false
	}
	var tagged = name != ""
	if !tagged {
		name = field.Name
	}
	return nil, _JSONField{name: name, kind: field.Type, tagged: tagged, index: index}, true
}

func fieldFor(target reflect.Type, key string) ([]int, bool) {
	var fields = jsonFields(target)
	var chosen = matchKey(len(fields), func(i int) string { return fields[i].name }, key)
	if chosen < 0 {
		return nil, false
	}
	return fields[chosen].index, true
}

func matchKey(count int, name func(int) string, key string) int {
	for i := range count {
		if name(i) == key {
			return i
		}
	}
	var folded = foldName(key)
	for i := range count {
		if foldName(name(i)) == folded {
			return i
		}
	}
	return -1
}

func foldName(name string) string {
	var folded strings.Builder
	for _, r := range name {
		switch {
		case r < utf8.RuneSelf && 'a' <= r && r <= 'z':
			folded.WriteRune(r - 'a' + 'A')
		case r < utf8.RuneSelf:
			folded.WriteRune(r)
		default:
			folded.WriteRune(unicode.ToUpper(unicode.ToLower(r)))
		}
	}
	return folded.String()
}

func validTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r) && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func dominantFields(found []_JSONField) []_JSONField {
	slices.SortStableFunc(found, func(a, b _JSONField) int {
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(len(a.index), len(b.index)), compareTagged(a, b))
	})
	var kept []_JSONField
	for start := 0; start < len(found); {
		var end = start + 1
		for end < len(found) && found[end].name == found[start].name {
			end++
		}
		var group = found[start:end]
		if len(group) == 1 || len(group[0].index) < len(group[1].index) || group[0].tagged != group[1].tagged {
			kept = append(kept, group[0])
		}
		start = end
	}
	slices.SortFunc(kept, func(a, b _JSONField) int { return slices.Compare(a.index, b.index) })
	return kept
}

func compareTagged(a, b _JSONField) int {
	switch {
	case a.tagged == b.tagged:
		return 0
	case a.tagged:
		return -1
	}
	return 1
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
	var chosen = matchKey(len(node.Fields), func(i int) string { return node.Fields[i].Name }, key)
	if chosen < 0 || node.Fields[chosen].Node < 0 {
		return nil
	}
	return &_ShapeCursor{shape: c.shape, node: node.Fields[chosen].Node}
}

func (c *_ShapeCursor) item() *_ShapeCursor {
	var node = c.current()
	if node == nil || node.Items == 0 {
		return nil
	}
	return &_ShapeCursor{shape: c.shape, node: node.Items - 1}
}
