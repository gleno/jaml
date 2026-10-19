package jaml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
)

const (
	maxOutlineSteps = 4096
	maxOutlineBytes = 64 << 10
)

type _Compiled struct {
	texts   map[string]_Text
	coded   map[any]bool
	encoded sync.Map
}

type _Encoded []byte

func identityOf(node any) any {
	switch typed := node.(type) {
	case *_Mapping:
		return typed
	case []any:
		if len(typed) == 0 {
			return nil
		}
		return &typed[0]
	}
	return nil
}

func (c *_Compiled) holdsCode(node any) bool {
	if written, isString := node.(string); isString {
		return c.texts[written].holdsCode()
	}
	return c.coded[identityOf(node)]
}

func (c *_Compiled) staticRoot(node any) any {
	var identity = identityOf(node)
	if identity == nil || c.coded[identity] {
		return nil
	}
	return node
}

func (c *_Compiled) spliced(root any, node any) (_Encoded, bool) {
	if root == nil {
		return nil, false
	}
	var encoding = c.encodingOf(root)
	if encoding == nil {
		return nil, false
	}
	span, recorded := encoding.extents[identityOf(node)]
	if !recorded {
		return nil, false
	}
	return encoding.written[span.start:span.end:span.end], true
}

func (c *_Compiled) encodingOf(root any) *_Encoding {
	var identity = identityOf(root)
	if known, seen := c.encoded.Load(identity); seen {
		return known.(*_Encoding)
	}
	var encoding = &_Encoding{extents: map[any]_Extent{}}
	var writer = unboundedWriter()
	if encoding.write(writer, root) != nil {
		encoding = nil
	} else {
		encoding.written = writer.written
	}
	known, _ := c.encoded.LoadOrStore(identity, encoding)
	return known.(*_Encoding)
}

type _Encoding struct {
	written []byte
	extents map[any]_Extent
}

type _Extent struct {
	start int
	end   int
}

func (e *_Encoding) write(w *_WireWriter, node any) error {
	var start = len(w.written)
	var err error
	switch typed := node.(type) {
	case *_Mapping:
		err = e.mapping(w, typed)
	case []any:
		err = e.list(w, typed)
	default:
		return w.data(node)
	}
	if err != nil {
		return err
	}
	if identity := identityOf(node); identity != nil {
		if _, recorded := e.extents[identity]; !recorded {
			e.extents[identity] = _Extent{start: start, end: len(w.written)}
		}
	}
	return nil
}

func (e *_Encoding) mapping(w *_WireWriter, mapping *_Mapping) error {
	w.mark('{')
	for index, key := range mapping.keys {
		if index > 0 {
			w.mark(',')
		}
		if err := w.quoted(key); err != nil {
			return err
		}
		w.mark(':')
		if err := e.write(w, mapping.values[key]); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (e *_Encoding) list(w *_WireWriter, list []any) error {
	if list == nil {
		return w.raw("null")
	}
	w.mark('[')
	for index, item := range list {
		if index > 0 {
			w.mark(',')
		}
		if err := e.write(w, item); err != nil {
			return err
		}
	}
	return w.raw("]")
}

type _CodeOutline struct {
	steps map[string]*_CodeOutline
}

func (o *_CodeOutline) holds(path []string) bool {
	for _, step := range path {
		if o == nil || o.steps == nil {
			break
		}
		o = o.steps[step]
	}
	return o != nil
}

type _Outlining struct {
	node    any
	outline *_CodeOutline
}

type _OutlineBudget struct {
	steps int
	bytes int
}

func (b *_OutlineBudget) spend(children []_Child) bool {
	var spent = _OutlineBudget{steps: b.steps + len(children), bytes: b.bytes}
	for _, child := range children {
		spent.bytes += len(child.step)
	}
	if spent.steps > maxOutlineSteps || spent.bytes > maxOutlineBytes {
		return false
	}
	*b = spent
	return true
}

func (c *_Compiled) outline(root any) *_CodeOutline {
	if !c.holdsCode(root) {
		return nil
	}
	var top = &_CodeOutline{}
	var queue []_Outlining
	if identityOf(root) != nil {
		queue = append(queue, _Outlining{node: root, outline: top})
	}
	var budget = _OutlineBudget{steps: 1}
	for len(queue) > 0 {
		var next = queue[0]
		queue = queue[1:]
		var coded = c.codedChildren(next.node)
		if !budget.spend(coded) {
			continue
		}
		next.outline.steps = make(map[string]*_CodeOutline, len(coded))
		for _, child := range coded {
			var outlined = &_CodeOutline{}
			next.outline.steps[child.step] = outlined
			if identityOf(child.node) != nil {
				queue = append(queue, _Outlining{node: child.node, outline: outlined})
			}
		}
	}
	return top
}

type _Child struct {
	step string
	node any
}

func (c *_Compiled) codedChildren(node any) []_Child {
	var coded []_Child
	switch typed := node.(type) {
	case *_Mapping:
		for _, key := range typed.keys {
			if c.holdsCode(typed.values[key]) {
				coded = append(coded, _Child{step: key, node: typed.values[key]})
			}
		}
	case []any:
		for index, item := range typed {
			if c.holdsCode(item) {
				coded = append(coded, _Child{step: strconv.Itoa(index), node: item})
			}
		}
	}
	return coded
}

type _OutlineWriting struct {
	steps map[string]*_CodeOutline
	keys  []string
}

func (o *_CodeOutline) MarshalJSON() ([]byte, error) {
	var w = unboundedWriter()
	var open []*_OutlineWriting
	var pending = o
	for {
		if next := writeOutlineValue(w, pending); next != nil {
			open = append(open, next)
		}
		for len(open) > 0 && len(open[len(open)-1].keys) == 0 {
			w.mark('}')
			open = open[:len(open)-1]
		}
		if len(open) == 0 {
			return w.written, nil
		}
		var top = open[len(open)-1]
		if len(top.keys) < len(top.steps) {
			w.mark(',')
		}
		var key = top.keys[0]
		top.keys = top.keys[1:]
		if err := w.quoted(key); err != nil {
			return nil, err
		}
		w.mark(':')
		pending = top.steps[key]
	}
}

func writeOutlineValue(w *_WireWriter, o *_CodeOutline) *_OutlineWriting {
	switch {
	case o == nil:
		w.written = append(w.written, "null"...)
	case o.steps == nil:
		w.written = append(w.written, "true"...)
	default:
		w.mark('{')
		return &_OutlineWriting{steps: o.steps, keys: slices.Sorted(maps.Keys(o.steps))}
	}
	return nil
}

func (o *_CodeOutline) UnmarshalJSON(written []byte) error {
	var tokens = json.NewDecoder(bytes.NewReader(written))
	first, err := readOutlineValue(tokens)
	if err != nil {
		return err
	}
	if first == nil {
		return errors.New("a code outline holds a null step")
	}
	*o = *first
	if o.steps == nil {
		return nil
	}
	var open = []*_CodeOutline{o}
	for len(open) > 0 {
		var top = open[len(open)-1]
		if !tokens.More() {
			if _, err := tokens.Token(); err != nil {
				return err
			}
			if len(top.steps) == 0 {
				return errors.New("a code outline step holds nothing")
			}
			open = open[:len(open)-1]
			continue
		}
		key, err := tokens.Token()
		if err != nil {
			return err
		}
		child, err := readOutlineValue(tokens)
		if err != nil {
			return err
		}
		top.steps[key.(string)] = child
		if child != nil && child.steps != nil {
			open = append(open, child)
		}
	}
	return nil
}

func readOutlineValue(tokens *json.Decoder) (*_CodeOutline, error) {
	token, err := tokens.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case true:
		return &_CodeOutline{}, nil
	case nil:
		return nil, nil
	case json.Delim('{'):
		return &_CodeOutline{steps: map[string]*_CodeOutline{}}, nil
	}
	return nil, fmt.Errorf("a code outline step is %v, neither true nor an object", token)
}

func (o *_CodeOutline) checked() error {
	var budget = _OutlineBudget{steps: 1}
	var pending = []*_CodeOutline{o}
	for len(pending) > 0 {
		var next = pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if next == nil {
			return errors.New("a code outline holds a null step")
		}
		var children = make([]_Child, 0, len(next.steps))
		for step, child := range next.steps {
			children = append(children, _Child{step: step})
			pending = append(pending, child)
		}
		if !budget.spend(children) {
			return fmt.Errorf("a code outline holds more than %d steps or %d bytes of steps", maxOutlineSteps, maxOutlineBytes)
		}
	}
	return nil
}
