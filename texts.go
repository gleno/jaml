package jaml

import (
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"
)

var (
	errInvalidText    = errors.New("holds text that is not valid UTF-8")
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

type _TextWalk struct {
	structs  map[reflect.Type]_KnownStruct
	followed map[_Followed]bool
}

type _KnownStruct struct {
	fields []_JSONField
	named  bool
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
	if !value.IsValid() || depth > maxDataDepth || isNil(value) || writesItsOwnJSON(value) {
		return true
	}
	if text, isText := marshalledText(value); isText {
		return utf8.Valid(text)
	}
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
		return utf8.ValidString(value.String())
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

func writesItsOwnJSON(value reflect.Value) bool {
	return implementsFor(value, marshalerType)
}

func marshalledText(value reflect.Value) ([]byte, bool) {
	if !implementsFor(value, textMarshalerType) {
		return nil, false
	}
	var receiver = value
	if !value.Type().Implements(textMarshalerType) {
		receiver = value.Addr()
	}
	if !receiver.CanInterface() {
		return nil, false
	}
	text, err := receiver.Interface().(encoding.TextMarshaler).MarshalText()
	if err != nil {
		return nil, false
	}
	return text, true
}

func implementsFor(value reflect.Value, behaviour reflect.Type) bool {
	return value.Type().Implements(behaviour) || value.CanAddr() && reflect.PointerTo(value.Type()).Implements(behaviour)
}

func writtenAsBytes(kind reflect.Type) bool {
	var item = kind.Elem()
	if item.Kind() != reflect.Uint8 {
		return false
	}
	var pointer = reflect.PointerTo(item)
	return !pointer.Implements(marshalerType) && !pointer.Implements(textMarshalerType)
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
		if !validKey(entry.Key()) || !w.valid(entry.Value(), depth) {
			return false
		}
	}
	return true
}

func validKey(key reflect.Value) bool {
	if key.Kind() == reflect.String {
		return utf8.ValidString(key.String())
	}
	if text, isText := marshalledText(key); isText {
		return utf8.Valid(text)
	}
	return true
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
	return true
}

func (w _TextWalk) structOf(kind reflect.Type) _KnownStruct {
	if known, seen := w.structs[kind]; seen {
		return known
	}
	var known = _KnownStruct{fields: jsonFields(kind), named: true}
	for _, field := range known.fields {
		known.named = known.named && !namedInInvalidText(kind.FieldByIndex(field.index).Tag)
	}
	w.structs[kind] = known
	return known
}

func namedInInvalidText(tag reflect.StructTag) bool {
	name, _, _ := strings.Cut(tag.Get("json"), ",")
	return strings.ContainsRune(name, utf8.RuneError) && !utf8.ValidString(string(tag))
}
