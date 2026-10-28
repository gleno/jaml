package jaml

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
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
		if skips == nil {
			return json.Unmarshal(settled.data, destination)
		}
		var sources = make(map[string]bool, len(settled.skipped))
		for _, path := range settled.skipped {
			sources[pathIdentity(path)] = true
		}
		var decoded = reflect.ValueOf(destination).Elem()
		var unmarshalView = json.UnmarshalFromFunc(func(decoder *jsontext.Decoder, view *Expression) error {
			if decoder.PeekKind() != '{' {
				return errors.ErrUnsupported
			}
			var path = nextDecodePath(decoder)
			if !sources[pathIdentity(path)] {
				// An opaque custom decoder may explicitly delegate a nested Expression.
				// Leave its values and options to the standard decoder unchanged.
				return errors.ErrUnsupported
			}
			if _, err := decoder.ReadValue(); err != nil {
				return err
			}
			view.held, view.path = nil, slices.Concat(e.path, path)
			return nil
		})
		// The local hook supplies source identity without adding methods to
		// Expression or changing native JSONv2 decoding semantics.
		var err = json.Unmarshal(settled.data, destination, json.WithUnmarshalers(unmarshalView))
		if err != nil {
			settleViews(decoded, &_ShapeCursor{shape: skips}, nil)
			return err
		}
		settleViews(decoded, &_ShapeCursor{shape: skips}, e.held)
		return nil
	})
}

func nextDecodePath(decoder *jsontext.Decoder) []string {
	var path = slices.Collect(decoder.StackPointer().Tokens())
	if kind, length := decoder.StackIndex(decoder.StackDepth()); kind == '[' {
		// StackPointer names the preceding array item, or the array itself when
		// empty. Object member names have already been read before their values.
		if length > 0 {
			path = path[:len(path)-1]
		}
		path = append(path, strconv.FormatInt(length, 10))
	}
	return path
}

func pathIdentity(path []string) string {
	// JSON's string-array encoding is unambiguous even for keys containing NUL,
	// separators, quotes, or decimal array indices. Nil and empty mean the root.
	if len(path) == 0 {
		return ""
	}
	var encoded, _ = json.Marshal(path)
	return string(encoded)
}

// Complete only library placeholders in the final decoded topology. The source
// path is already attached to each leaf, so map-key decoders are never replayed.
// On failure the old null representation is restored in partial results.
func settleViews(value reflect.Value, cursor *_ShapeCursor, held *_Handle) bool {
	if cursor == nil {
		return false
	}
	if cursor.skips() {
		if value.IsNil() {
			return false
		}
		var view = value.Interface().(*Expression)
		if view.held != nil {
			return false
		}
		if held != nil {
			view.held = held
			return false
		}
		value.SetZero()
		return true
	}
	var changed bool
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			return settleViews(value.Elem(), cursor, held)
		}
	case reflect.Struct:
		var layout = jsonLayout(value.Type())
		for _, field := range layout.fields {
			if found, readable := fieldAt(value, field.index); readable && settleViews(found, cursor.key(field.name), held) {
				changed = true
			}
		}
		if field := layout.fallback; field != nil {
			if found, readable := fieldAt(value, field.index); readable {
				if found.Kind() == reflect.Pointer && !found.IsNil() {
					found = found.Elem()
				}
				if found.Kind() == reflect.Map && settleMapViews(found, cursor.fallback(), held) {
					changed = true
				}
			}
		}
	case reflect.Map:
		return settleMapViews(value, cursor.item(), held)
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if settleViews(value.Index(index), cursor.item(), held) {
				changed = true
			}
		}
	}
	return changed
}

func settleMapViews(value reflect.Value, cursor *_ShapeCursor, held *_Handle) bool {
	if cursor == nil {
		return false
	}
	var changed bool
	for entries := value.MapRange(); entries.Next(); {
		var entry = reflect.New(value.Type().Elem()).Elem()
		entry.Set(entries.Value())
		if settleViews(entry, cursor, held) {
			value.SetMapIndex(entries.Key(), entry)
			changed = true
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
