package jaml

import (
	"context"
	"encoding"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"
)

func Resolve[T any](ctx context.Context, e *Expression, args ...any) (T, error) {
	var result T
	var err = e.resolve(ctx, &result, args)
	return result, err
}

func (e *Expression) resolve(ctx context.Context, destination any, args []any) error {
	if e == nil || e.held == nil {
		return unmadeExpression("Resolve")
	}
	if err := admitPath(e.path); err != nil {
		return err
	}
	if err := e.held.admit(ctx, selectedPath(e.path).String()); err != nil {
		return err
	}
	var target = reflect.TypeOf(destination).Elem()
	skips, err := skipsFor(target)
	if err != nil {
		return err
	}
	timeLimit, err := timeLimitOf(ctx)
	if err != nil {
		return err
	}
	var ask = _Ask{path: e.path, values: valuesOf(ctx), scripts: scriptsOf(ctx), args: args, skips: skips, timeLimit: timeLimit}
	return e.held.answer(ctx, ask, func(settled _Answered) error {
		if err := json.Unmarshal(settled.data, destination); err != nil {
			return err
		}
		var decoded = reflect.ValueOf(destination).Elem()
		for _, path := range settled.skipped {
			bindView(decoded, path, &Expression{held: e.held, path: slices.Concat(e.path, path)})
		}
		return nil
	})
}

func admitScripts(scripts []string) ([]string, error) {
	for index, source := range scripts {
		switch {
		case !utf8.ValidString(source):
			return nil, fail(InvalidExpression, "script %d: %w", index+1, syntaxErrorAt(source, firstInvalidByte(source), "Invalid UTF-8 character"))
		case len(source) > maxCodeBytes:
			return nil, fail(InvalidExpression, "script %d is longer than %d bytes", index+1, maxCodeBytes)
		}
	}
	return scripts, nil
}

func skipsFor(target reflect.Type) (*_Shape, error) {
	nodes, err := shapeOf(target)
	if err != nil || nodes == nil {
		return nil, err
	}
	return &_Shape{Nodes: nodes}, nil
}

func bindView(value reflect.Value, path []string, view *Expression) {
	if len(path) == 0 {
		if value.Type() == expressionPointer {
			value.Set(reflect.ValueOf(view))
		}
		return
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		bindView(value.Elem(), path, view)
	case reflect.Struct:
		if field, found := fieldNamed(value, path[0]); found {
			bindView(field, path[1:], view)
		}
	case reflect.Map:
		key, readable := mapKey(value.Type().Key(), path[0])
		if !readable {
			return
		}
		var held = reflect.New(value.Type().Elem()).Elem()
		if existing := value.MapIndex(key); existing.IsValid() {
			held.Set(existing)
		}
		bindView(held, path[1:], view)
		value.SetMapIndex(key, held)
	case reflect.Slice, reflect.Array:
		if index, err := strconv.Atoi(path[0]); err == nil && index < value.Len() {
			bindView(value.Index(index), path[1:], view)
		}
	}
}

func mapKey(kind reflect.Type, written string) (reflect.Value, bool) {
	var key = reflect.New(kind)
	if unmarshaler, decodes := key.Interface().(encoding.TextUnmarshaler); decodes {
		return key.Elem(), unmarshaler.UnmarshalText([]byte(written)) == nil
	}
	switch kind.Kind() {
	case reflect.String:
		return reflect.ValueOf(written).Convert(kind), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, err := strconv.ParseInt(written, 10, kind.Bits())
		key.Elem().SetInt(number)
		return key.Elem(), err == nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		number, err := strconv.ParseUint(written, 10, kind.Bits())
		key.Elem().SetUint(number)
		return key.Elem(), err == nil
	}
	return reflect.Value{}, false
}

func fieldNamed(value reflect.Value, key string) (reflect.Value, bool) {
	index, found := fieldFor(value.Type(), key)
	if !found {
		return reflect.Value{}, false
	}
	return fieldAt(value, index)
}

func fieldAt(value reflect.Value, index []int) (reflect.Value, bool) {
	for _, step := range index {
		if value.Kind() == reflect.Pointer {
			if value.IsNil() && !value.CanSet() {
				return reflect.Value{}, false
			}
			if value.IsNil() {
				value.Set(reflect.New(value.Type().Elem()))
			}
			value = value.Elem()
		}
		value = value.Field(step)
	}
	return value, value.CanSet()
}
