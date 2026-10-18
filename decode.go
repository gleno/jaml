package jaml

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/gleno/fault"
)

var (
	durationType        = reflect.TypeFor[time.Duration]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

type destination struct {
	target reflect.Value
	at     location
}

func (d destination) to(target reflect.Value) destination {
	return destination{target: target, at: d.at}
}

func (e *evaluation) decode(v value, into destination) error {
	resolved, err := e.resolve(v, into.at)
	if err != nil {
		return err
	}
	switch resolved.(type) {
	case nullValue, undefinedValue:
		return e.decodeAbsent(resolved, into)
	}
	target := into.target
	if target.Kind() == reflect.Pointer {
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		return e.decode(resolved, into.to(target.Elem()))
	}
	if decoded, err := e.decodeSpecial(resolved, into); decoded || err != nil {
		return err
	}
	return e.decodeByKind(resolved, into)
}

func (e *evaluation) decodeAbsent(absent value, into destination) error {
	if e.coercionOf(absent) == Loose || (isNull(absent) && isNullable(into.target.Type())) {
		into.target.SetZero()
		return nil
	}
	return fault.From(CoercionFailed, fmt.Sprintf("%s: strict %s coercion refuses %s for %s", into.at, absent.kind(), absent.kind(), into.target.Type()))
}

func isNull(v value) bool {
	_, null := v.(nullValue)
	return null
}

func isNullable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return true
	default:
		return false
	}
}

func (e *evaluation) decodeSpecial(resolved value, into destination) (bool, error) {
	target := into.target
	if target.Type() == durationType {
		return true, e.decodeDuration(resolved, into)
	}
	if !target.CanAddr() {
		return false, nil
	}
	pointer := target.Addr()
	if pointer.Type().Implements(jsonUnmarshalerType) {
		return true, e.decodeJSONUnmarshaler(resolved, into)
	}
	if pointer.Type().Implements(textUnmarshalerType) {
		return true, e.decodeTextUnmarshaler(resolved, into)
	}
	return false, nil
}

func (e *evaluation) decodeDuration(resolved value, into destination) error {
	text, isText := resolved.(stringValue)
	if !isText {
		return mismatch(resolved, into, "durations are written as text like \"1m30s\"")
	}
	duration, err := time.ParseDuration(string(text))
	if err != nil {
		return mismatch(resolved, into, err.Error())
	}
	into.target.SetInt(int64(duration))
	return nil
}

func (e *evaluation) decodeJSONUnmarshaler(resolved value, into destination) error {
	natural, err := e.natural(resolved, into.at)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(natural)
	if err != nil {
		return mismatch(resolved, into, err.Error())
	}
	if err := into.target.Addr().Interface().(json.Unmarshaler).UnmarshalJSON(encoded); err != nil {
		return mismatch(resolved, into, err.Error())
	}
	return nil
}

func (e *evaluation) decodeTextUnmarshaler(resolved value, into destination) error {
	switch resolved.(type) {
	case listValue, objectValue:
		return mismatch(resolved, into, "")
	}
	text, err := e.stringify(resolved, into.at)
	if err != nil {
		return err
	}
	if err := into.target.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(text)); err != nil {
		return mismatch(resolved, into, err.Error())
	}
	return nil
}

func (e *evaluation) decodeByKind(resolved value, into destination) error {
	target := into.target
	switch target.Kind() {
	case reflect.Interface:
		return e.decodeInterface(resolved, into)
	case reflect.String:
		text, err := e.stringify(resolved, into.at)
		if err == nil {
			target.SetString(text)
		}
		return err
	case reflect.Bool:
		return decodeBool(resolved, into)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decodeInt(resolved, into)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return decodeUint(resolved, into)
	case reflect.Float32, reflect.Float64:
		return decodeFloat(resolved, into)
	case reflect.Struct:
		return e.decodeStruct(resolved, into)
	case reflect.Map:
		return e.decodeMap(resolved, into)
	case reflect.Slice:
		return e.decodeSlice(resolved, into)
	case reflect.Array:
		return e.decodeArray(resolved, into)
	default:
		return unsupported(into)
	}
}

func (e *evaluation) decodeInterface(resolved value, into destination) error {
	if into.target.NumMethod() != 0 {
		return unsupported(into)
	}
	natural, err := e.natural(resolved, into.at)
	if err != nil {
		return err
	}
	if natural == nil {
		into.target.SetZero()
		return nil
	}
	into.target.Set(reflect.ValueOf(natural))
	return nil
}

func (e *evaluation) natural(resolved value, at location) (any, error) {
	switch typed := resolved.(type) {
	case boolValue:
		return typed.truth, nil
	case stringValue:
		return string(typed), nil
	case numberValue:
		return naturalNumber(typed.text), nil
	case unrepresentable:
		return nil, fault.From(CoercionFailed, fmt.Sprintf("%s: %s", at, typed.reason))
	case listValue:
		items := make([]any, len(typed))
		for i, item := range typed {
			if err := e.decode(item.value, destination{target: reflect.ValueOf(&items[i]).Elem(), at: at.index(i, item.at)}); err != nil {
				return nil, err
			}
		}
		return items, nil
	case objectValue:
		entries := make(map[string]any, len(typed))
		for _, entry := range typed {
			var item any
			if err := e.decode(entry.value, destination{target: reflect.ValueOf(&item).Elem(), at: at.key(entry.key, entry.at)}); err != nil {
				return nil, err
			}
			entries[entry.key] = item
		}
		return entries, nil
	default:
		return nil, nil
	}
}

func (e *evaluation) decodeStruct(resolved value, into destination) error {
	object, isObject := resolved.(objectValue)
	if !isObject {
		return mismatch(resolved, into, "")
	}
	fields := fieldsOf(into.target.Type())
	for _, entry := range object {
		at := into.at.key(entry.key, entry.at)
		declared, known := fields[entry.key]
		if !known {
			return fault.From(CoercionFailed, fmt.Sprintf("%s: %s has no field %q", at, into.target.Type(), entry.key))
		}
		target := fieldByIndexAllocating(into.target, declared.index)
		if err := e.decode(entry.value, destination{target: target, at: at}); err != nil {
			return err
		}
	}
	return nil
}

func (e *evaluation) decodeMap(resolved value, into destination) error {
	object, isObject := resolved.(objectValue)
	if !isObject {
		return mismatch(resolved, into, "")
	}
	mapType := into.target.Type()
	if mapType.Key().Kind() != reflect.String {
		return unsupported(into)
	}
	if into.target.IsNil() {
		into.target.Set(reflect.MakeMapWithSize(mapType, len(object)))
	}
	for _, entry := range object {
		item := reflect.New(mapType.Elem()).Elem()
		if err := e.decode(entry.value, destination{target: item, at: into.at.key(entry.key, entry.at)}); err != nil {
			return err
		}
		into.target.SetMapIndex(reflect.ValueOf(entry.key).Convert(mapType.Key()), item)
	}
	return nil
}

func (e *evaluation) decodeSlice(resolved value, into destination) error {
	list, isList := resolved.(listValue)
	if !isList {
		return mismatch(resolved, into, "")
	}
	slice := reflect.MakeSlice(into.target.Type(), len(list), len(list))
	if err := e.decodeItems(list, destination{target: slice, at: into.at}); err != nil {
		return err
	}
	into.target.Set(slice)
	return nil
}

func (e *evaluation) decodeArray(resolved value, into destination) error {
	list, isList := resolved.(listValue)
	if !isList {
		return mismatch(resolved, into, "")
	}
	if len(list) != into.target.Len() {
		return mismatch(resolved, into, fmt.Sprintf("it holds %d items, the array %d", len(list), into.target.Len()))
	}
	return e.decodeItems(list, into)
}

func (e *evaluation) decodeItems(list listValue, into destination) error {
	for i, item := range list {
		if err := e.decode(item.value, destination{target: into.target.Index(i), at: into.at.index(i, item.at)}); err != nil {
			return err
		}
	}
	return nil
}

func mismatch(resolved value, into destination, reason string) error {
	message := fmt.Sprintf("%s: cannot coerce %s into %s", into.at, describeValue(resolved), into.target.Type())
	if reason != "" {
		message += ": " + reason
	}
	return fault.From(CoercionFailed, message)
}

func unsupported(into destination) error {
	return fault.From(InvalidTarget, fmt.Sprintf("%s: %s is not a supported target type", into.at, into.target.Type()))
}
