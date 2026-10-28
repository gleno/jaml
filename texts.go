package jaml

import (
	"encoding"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"
)

var (
	errInvalidText      = errors.New("holds text that is not valid UTF-8")
	streamMarshalerType = reflect.TypeFor[jsonv2.MarshalerTo]()
	inputMarshalerTypes = [...]reflect.Type{
		streamMarshalerType,
		reflect.TypeFor[json.Marshaler](),
		reflect.TypeFor[encoding.TextAppender](),
		reflect.TypeFor[encoding.TextMarshaler](),
	}
)

type _TextWalk struct {
	structs   map[reflect.Type]_KnownStruct
	followed  map[_Followed]bool
	namesOnly bool
}

type _KnownStruct struct {
	fields   []_JSONField
	fallback *_JSONField
	named    bool
}

type _Followed struct {
	address uintptr
	kind    reflect.Type
	length  int
}

func holdsValidText(value any) bool {
	var walk = _TextWalk{structs: map[reflect.Type]_KnownStruct{}, followed: map[_Followed]bool{}}
	return walk.valid(reflect.ValueOf(value), 0)
}

func (w _TextWalk) valid(value reflect.Value, depth int) bool {
	if !value.IsValid() || depth > maxDataDepth || isNil(value) || marshalsItself(value) {
		return true
	}
	// MarshalJSONTo may decline and let the encoder use ordinary fields. Preserve
	// malformed-tag rejection in that fallback without invoking the method.
	if implementsFor(value, streamMarshalerType) {
		w.namesOnly = true
	}
	return w.contents(value, depth)
}

func (w _TextWalk) contents(value reflect.Value, depth int) bool {
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		var followed = _Followed{address: value.Pointer(), kind: value.Type()}
		if value.Kind() == reflect.Slice {
			followed.length = value.Len()
		}
		if w.followed[followed] {
			return true
		}
		w.followed[followed] = true
		defer delete(w.followed, followed)
	}
	switch value.Kind() {
	case reflect.String:
		return w.namesOnly || utf8.ValidString(value.String())
	case reflect.Interface:
		return w.valid(value.Elem(), depth)
	case reflect.Pointer:
		return w.valid(value.Elem(), depth)
	case reflect.Slice:
		return writtenAsBytes(value.Type()) || w.validItems(value, depth+1)
	case reflect.Array:
		return w.validItems(value, depth+1)
	case reflect.Map:
		return w.validEntries(value, depth+1)
	case reflect.Struct:
		return w.validFields(value, depth+1)
	}
	return true
}

func isNil(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return value.IsNil()
	}
	return false
}

// Only the JSON encoder calls application methods and validates their text.
// MarshalJSONTo remains visible to the metadata walk because it may decline.
func marshalsItself(value reflect.Value) bool {
	for _, behavior := range inputMarshalerTypes {
		if behavior != streamMarshalerType && implementsFor(value, behavior) {
			return true
		}
	}
	return false
}

func implementsFor(value reflect.Value, behavior reflect.Type) bool {
	return value.Type().Implements(behavior) || value.CanAddr() && reflect.PointerTo(value.Type()).Implements(behavior)
}

func hasInputMarshaler(kind reflect.Type) bool {
	for _, behavior := range inputMarshalerTypes {
		if kind.Implements(behavior) {
			return true
		}
	}
	return false
}

func writtenAsBytes(kind reflect.Type) bool {
	var item = kind.Elem()
	if item.Kind() != reflect.Uint8 {
		return false
	}
	return !hasInputMarshaler(reflect.PointerTo(item))
}

func (w _TextWalk) validItems(items reflect.Value, depth int) bool {
	for index := range items.Len() {
		if !w.valid(items.Index(index), depth) {
			return false
		}
	}
	return true
}

func (w _TextWalk) validEntries(entries reflect.Value, depth int) bool {
	for entry := entries.MapRange(); entry.Next(); {
		if !w.namesOnly && !validKey(entry.Key()) || !w.valid(entry.Value(), depth) {
			return false
		}
	}
	return true
}

func validKey(key reflect.Value) bool {
	// String keys ignore custom methods; other keys are checked after encoding.
	return key.Kind() != reflect.String || utf8.ValidString(key.String())
}

func (w _TextWalk) validFields(object reflect.Value, depth int) bool {
	var known = w.structOf(object.Type())
	if !known.named {
		return false
	}
	for _, field := range known.fields {
		value, err := object.FieldByIndexErr(field.index)
		if err == nil && !w.valid(value, depth) {
			return false
		}
	}
	if known.fallback != nil {
		value, err := object.FieldByIndexErr(known.fallback.index)
		if err == nil && !w.validFallback(value, depth) {
			return false
		}
	}
	return true
}

func (w _TextWalk) validFallback(value reflect.Value, depth int) bool {
	if !value.IsValid() || depth > maxDataDepth || isNil(value) {
		return true
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Map {
		return w.valid(value, depth)
	}
	// Native embedded maps ignore their own methods. Inspect their entries
	// through the same cycle/depth walk, keeping ordinary value methods opaque.
	return w.contents(value, depth)
}

func (w _TextWalk) structOf(kind reflect.Type) _KnownStruct {
	if known, seen := w.structs[kind]; seen {
		return known
	}
	var layout = jsonLayout(kind)
	var known = _KnownStruct{fields: layout.fields, fallback: layout.fallback, named: true}
	for _, field := range jsonFields(kind) {
		known.named = known.named && !namedInInvalidText(kind.FieldByIndex(field.index).Tag)
	}
	w.structs[kind] = known
	return known
}

func namedInInvalidText(tag reflect.StructTag) bool {
	name, _, _ := strings.Cut(tag.Get("json"), ",")
	return strings.ContainsRune(name, utf8.RuneError) && !utf8.ValidString(string(tag))
}
