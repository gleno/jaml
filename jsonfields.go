// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license in LICENSE-Go.
//
// Field discovery and tag consumption are adapted from Go 1.27.3
// src/encoding/json/v2/fields.go. This contains only the metadata needed by
// JAML; values and application methods are always handled by the native codec.
package jaml

import (
	"cmp"
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type _JSONField struct {
	name   string
	kind   reflect.Type
	tagged bool
	strict bool
	index  []int
	order  int
}

type _JSONFields struct {
	fields   []_JSONField // native depth-first traversal order
	lookup   []int        // native breadth-first folded-match precedence
	fallback *_JSONField
}

var (
	jsonFieldLayouts sync.Map
	jsonValueType    = reflect.TypeFor[jsontext.Value]()
	zeroerType       = reflect.TypeFor[interface{ IsZero() bool }]()
	jsonMethodTypes  = []reflect.Type{
		reflect.TypeFor[json.MarshalerTo](), reflect.TypeFor[json.Marshaler](),
		reflect.TypeFor[encoding.TextAppender](), reflect.TypeFor[encoding.TextMarshaler](),
		reflect.TypeFor[json.UnmarshalerFrom](), reflect.TypeFor[json.Unmarshaler](),
		reflect.TypeFor[encoding.TextUnmarshaler](),
	}
)

func jsonLayout(target reflect.Type) *_JSONFields {
	if known, found := jsonFieldLayouts.Load(target); found {
		return known.(*_JSONFields)
	}
	var made = makeJSONFields(target)
	known, _ := jsonFieldLayouts.LoadOrStore(target, made)
	return known.(*_JSONFields)
}

func makeJSONFields(target reflect.Type) *_JSONFields {
	type _Queued struct {
		kind     reflect.Type
		index    []int
		children bool
	}
	var queue = []_Queued{{kind: target, children: true}}
	var seen = map[reflect.Type]bool{target: true}
	var found, fallbacks []_JSONField
	for next := 0; next < len(queue); next++ {
		var parent = queue[next]
		for i := range parent.kind.NumField() {
			var declared = parent.kind.Field(i)
			options, ignored := jsonFieldOptions(declared)
			if ignored {
				continue
			}
			var field = _JSONField{name: options.name, kind: declared.Type, tagged: options.named, strict: options.casing != 1, index: append(slices.Clone(parent.index), i)}
			var underlying = embeddedType(declared.Type)
			var embed = options.embed || declared.Anonymous && !options.named && underlying.Kind() == reflect.Struct
			if embed && !options.named {
				// Invalid metadata is rejected by the native codec before this
				// layout is used for lazy fields.
				field.strict, options.omitzero = true, false
				if underlying.Kind() == reflect.Struct {
					if parent.children {
						queue = append(queue, _Queued{underlying, field.index, !seen[underlying]})
					}
					seen[underlying] = true
					continue
				}
				if !declared.IsExported() {
					continue
				}
				if underlying == jsonValueType || underlying.Kind() == reflect.Map && underlying.Key().Kind() == reflect.String && !hasJSONMethods(underlying.Key()) {
					fallbacks = append(fallbacks, field)
					continue
				}
				// Unsupported embedding is rejected by native metadata validation.
			}
			if !declared.IsExported() {
				if !declared.Anonymous || underlying.Kind() != reflect.Struct || hasJSONMethods(underlying) || options.omitzero && implementsJSONInterface(underlying, zeroerType) {
					continue
				}
			}
			field.order = len(found)
			found = append(found, field)
		}
	}
	var layout = &_JSONFields{fields: dominantFields(found)}
	for i := range layout.fields {
		layout.lookup = append(layout.lookup, i)
	}
	slices.SortFunc(layout.lookup, func(a, b int) int { return cmp.Compare(layout.fields[a].order, layout.fields[b].order) })
	if len(fallbacks) == 1 || len(fallbacks) > 1 && len(fallbacks[0].index) != len(fallbacks[1].index) {
		layout.fallback = &fallbacks[0]
	}
	return layout
}

func embeddedType(kind reflect.Type) reflect.Type {
	if kind.Kind() == reflect.Pointer && kind.Name() == "" {
		return kind.Elem()
	}
	return kind
}

func implementsJSONInterface(kind, behavior reflect.Type) bool {
	return kind.Implements(behavior) || reflect.PointerTo(kind).Implements(behavior)
}

func hasJSONMethods(kind reflect.Type) bool {
	return slices.ContainsFunc(jsonMethodTypes, func(behavior reflect.Type) bool { return implementsJSONInterface(kind, behavior) })
}

type _JSONFieldOptions struct {
	name     string
	named    bool
	embed    bool
	omitzero bool
	casing   int
}

// Extract only discovery metadata. The native codec validates tags before
// shapes are drawn; it also owns all input validation and method calls.
func jsonFieldOptions(field reflect.StructField) (_JSONFieldOptions, bool) {
	var tag = field.Tag.Get("json")
	var out = _JSONFieldOptions{name: field.Name}
	if tag == "-" || !field.IsExported() && !field.Anonymous {
		return out, true
	}
	if len(tag) > 0 && tag[0] != ',' {
		var n = len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool { return !strings.ContainsRune(",\\'\"`", r) }))
		name, valid := tag[:n], true
		if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
			name, n, valid = consumeJSONTagOption(tag, false)
		}
		if !utf8.ValidString(name) {
			name = string([]rune(name))
		}
		if valid {
			out.name, out.named = name, true
		}
		tag = tag[n:]
	}
	for len(tag) > 0 {
		if tag[0] == ',' {
			tag = tag[1:]
			if tag == "" {
				break
			}
		}
		option, n, _ := consumeJSONTagOption(tag, false)
		tag = tag[n:]
		switch option {
		case "embed":
			out.embed = true
		case "omitzero":
			out.omitzero = true
		case "case", "format":
			if !strings.HasPrefix(tag, ":") {
				continue
			}
			var value string
			var valid bool
			value, n, valid = consumeJSONTagOption(tag[1:], option == "format")
			tag = tag[1+n:]
			if valid && option == "case" {
				switch value {
				case "ignore":
					out.casing |= 1
				case "strict":
					out.casing |= 2
				}
			}
		}
	}
	return out, false
}

func consumeJSONTagOption(in string, quoted bool) (string, int, bool) {
	var end = strings.IndexByte(in, ',')
	if end < 0 {
		end = len(in)
	}
	switch r, _ := utf8.DecodeRuneInString(in); {
	case r == '_' || unicode.IsLetter(r):
		var n = len(in) - len(strings.TrimLeftFunc(in, func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }))
		return in[:n], n, true
	case r == '\'' && quoted:
		var escaped bool
		var text = []byte{'"'}
		for n := 1; n < len(in); {
			r, width := utf8.DecodeRuneInString(in[n:])
			switch {
			case escaped:
				if r == '\'' {
					text = text[:len(text)-1]
				}
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				text = append(text, '\\')
			case r == '\'':
				value, err := strconv.Unquote(string(append(text, '"')))
				if err == nil {
					return value, n + width, true
				}
				return in[:end], end, false
			}
			text = append(text, in[n:n+width]...)
			n += width
		}
	}
	return in[:end], end, false
}

func fieldFor(target reflect.Type, key string) ([]int, bool) {
	var layout = jsonLayout(target)
	var chosen = matchKey(len(layout.lookup), func(i int) string { return layout.fields[layout.lookup[i]].name }, func(i int) bool { return layout.fields[layout.lookup[i]].strict }, key)
	if chosen < 0 {
		return nil, false
	}
	return layout.fields[layout.lookup[chosen]].index, true
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
