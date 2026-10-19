package jaml

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

type renderer struct {
	texts    map[string]text
	settings settings
	engine   *lazySession
}

func (r *renderer) render(node any, at *dataPath) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		rendered := make(map[string]any, len(typed))
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			value, err := r.render(typed[key], at.atKey(key))
			if err != nil {
				return nil, err
			}
			rendered[key] = value
		}
		return rendered, nil
	case []any:
		rendered := make([]any, len(typed))
		for index, item := range typed {
			value, err := r.render(item, at.atIndex(index))
			if err != nil {
				return nil, err
			}
			rendered[index] = value
		}
		return rendered, nil
	case string:
		compiled, hasCode := r.texts[typed]
		if !hasCode {
			return typed, nil
		}
		return r.renderText(compiled, at)
	default:
		return node, nil
	}
}

func (r *renderer) renderText(compiled text, at *dataPath) (any, error) {
	if compiled.whole != nil {
		result, err := r.evaluate(*compiled.whole, at)
		if err != nil {
			return nil, err
		}
		return r.admit(result, *compiled.whole, at)
	}
	var rendered strings.Builder
	for _, piece := range compiled.segments {
		if piece.code == nil {
			rendered.WriteString(piece.literal)
			continue
		}
		result, err := r.evaluate(*piece.code, at)
		if err != nil {
			return nil, err
		}
		admitted, err := r.admit(result, *piece.code, at)
		if err != nil {
			return nil, err
		}
		written, err := stringify(admitted)
		if err != nil {
			return nil, fail(EvaluationFailed, "%s: %s: %w", at, piece.code.written(), err)
		}
		rendered.WriteString(written)
	}
	return rendered.String(), nil
}

func (r *renderer) admit(result any, compiled expression, at *dataPath) (any, error) {
	absent, coercion := "", Loose
	switch result.(type) {
	case nil:
		absent, coercion = "null", r.settings.null
	case undefined:
		absent, coercion = "undefined", r.settings.undefined
	default:
		return result, nil
	}
	if coercion == Strict {
		return nil, fail(CoercionFailed, "%s: %s is %s, which strict %s coercion refuses", at, compiled.written(), absent, absent)
	}
	return nil, nil
}

func (r *renderer) evaluate(compiled expression, at *dataPath) (any, error) {
	session, err := r.engine.open()
	if err != nil {
		return nil, err
	}
	result, err := session.evaluate(compiled)
	if err != nil {
		return nil, fail(EvaluationFailed, "%s: %s: %w", at, compiled.written(), err)
	}
	return result, nil
}

func (r *renderer) call(compiled expression, args []json.RawMessage) (any, error) {
	session, err := r.engine.open()
	if err != nil {
		return nil, err
	}
	result, err := session.call(compiled, args)
	if err != nil {
		return nil, fail(EvaluationFailed, "%s: %s: %w", (*dataPath)(nil), compiled.written(), err)
	}
	return r.admit(result, compiled, nil)
}

func stringify(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	default:
		return dataText(typed)
	}
}
