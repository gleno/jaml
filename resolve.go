package jaml

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
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
	return placed(e.selection(), e.decodeInto(ctx, destination, args))
}

func (e *Expression) decodeInto(ctx context.Context, destination any, args []any) error {
	if e == nil || e.held == nil {
		return unmadeExpression("Resolve")
	}
	if err := admitPath(e.path); err != nil {
		return err
	}
	if err := e.held.admit(ctx); err != nil {
		return err
	}
	var target = reflect.TypeOf(destination).Elem()
	skips, err := skipsFor(target)
	if err != nil {
		return err
	}
	wallLimit, err := wallLimitOf(ctx)
	if err != nil {
		return err
	}
	timeLimit, err := timeLimitOf(ctx)
	if err != nil {
		return err
	}
	var ask = _Ask{path: e.path, values: valuesOf(ctx), scripts: scriptsOf(ctx), args: args, skips: skips, wallLimit: wallLimit, timeLimit: timeLimit}
	return e.held.answer(ctx, ask, func(settled _Answered) error {
		var decoded = reflect.ValueOf(destination).Elem()
		if err := json.Unmarshal(settled.data, destination); err != nil {
			if skips != nil {
				discardViews(decoded, &_ShapeCursor{shape: skips})
			}
			return err
		}
		for _, path := range settled.skipped {
			if view := viewAt(decoded, path); view != nil {
				view.held, view.path = e.held, slices.Concat(e.path, path)
			}
		}
		return nil
	})
}

// A failed decode historically left null at lazy fields. Clear only placeholders
// in the final decoded topology, without replaying source paths or map decoders.
func discardViews(value reflect.Value, cursor *_ShapeCursor) bool {
	if cursor == nil {
		return false
	}
	if cursor.skips() {
		if !value.IsNil() && value.Interface().(*Expression).held == nil {
			value.SetZero()
			return true
		}
		return false
	}
	var changed bool
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			return discardViews(value.Elem(), cursor)
		}
	case reflect.Struct:
		for _, field := range jsonFields(value.Type()) {
			if held, found := fieldAt(value, field.index); found && discardViews(held, cursor.key(field.name)) {
				changed = true
			}
		}
	case reflect.Map:
		for entries := value.MapRange(); entries.Next(); {
			var held = reflect.New(value.Type().Elem()).Elem()
			held.Set(entries.Value())
			if discardViews(held, cursor.item()) {
				value.SetMapIndex(entries.Key(), held)
				changed = true
			}
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if discardViews(value.Index(index), cursor.item()) {
				changed = true
			}
		}
	}
	return changed
}

func admitScripts(scripts []string) ([]string, error) {
	for index, source := range scripts {
		switch {
		case !utf8.ValidString(source):
			return nil, _Unplaced{kind: invalidExpression, label: scriptLabel(index), cause: syntaxErrorAt(source, firstInvalidByte(source), "Invalid UTF-8 character")}
		case len(source) > maxCodeBytes:
			return nil, _Unplaced{kind: invalidExpression, label: scriptLabel(index), cause: fmt.Errorf("longer than %d bytes", maxCodeBytes)}
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

func viewAt(value reflect.Value, path []string) *Expression {
	if len(path) == 0 {
		if value.Type() == expressionPointer && !value.IsNil() {
			return value.Interface().(*Expression)
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			return viewAt(value.Elem(), path)
		}
	case reflect.Struct:
		if field, found := fieldNamed(value, path[0]); found {
			return viewAt(field, path[1:])
		}
	case reflect.Map:
		key, readable := mapKey(value.Type().Key(), path[0])
		if !readable {
			return nil
		}
		if existing := value.MapIndex(key); existing.IsValid() {
			return viewAt(existing, path[1:])
		}
	case reflect.Slice, reflect.Array:
		if index, err := strconv.Atoi(path[0]); err == nil && index >= 0 && index < value.Len() {
			return viewAt(value.Index(index), path[1:])
		}
	}
	return nil
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
			if value.IsNil() {
				return reflect.Value{}, false
			}
			value = value.Elem()
		}
		value = value.Field(step)
	}
	return value, value.CanInterface()
}
