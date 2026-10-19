package jaml

import (
	"slices"
	"strconv"
	"strings"
)

type renderer struct {
	texts    map[string]text
	settings _ExpressionSettings
	engine   *lazySession
	args     []datum
	skips    *_Shape
	depth    int
	skipped  [][]string
}

type _Place struct {
	at    *dataPath
	skips *_ShapeCursor
}

func (r *renderer) place(at *dataPath) _Place {
	if r.skips == nil {
		return _Place{at: at}
	}
	return _Place{at: at, skips: &_ShapeCursor{shape: r.skips}}
}

func (p _Place) key(key string) _Place {
	return _Place{at: p.at.atKey(key), skips: p.skips.key(key)}
}

func (p _Place) item(index int) _Place {
	return _Place{at: p.at.atIndex(index), skips: p.skips.item()}
}

func (r *renderer) render(node any, place _Place) (any, error) {
	if place.skips.skips() {
		r.skipped = append(r.skipped, r.relative(place.at))
		return map[string]any{}, nil
	}
	switch typed := node.(type) {
	case *_Mapping:
		var rendered = &_Mapping{keys: typed.keys, values: make(map[string]any, len(typed.keys))}
		for _, key := range typed.keys {
			value, err := r.render(typed.values[key], place.key(key))
			if err != nil {
				return nil, err
			}
			rendered.values[key] = value
		}
		return rendered, nil
	case []any:
		rendered := make([]any, len(typed))
		for index, item := range typed {
			value, err := r.render(item, place.item(index))
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
		return r.renderText(compiled, place.at)
	default:
		return node, nil
	}
}

func (r *renderer) relative(at *dataPath) []string {
	var steps []string
	for step := at; step.depth() > r.depth; step = step.parent {
		if step.keyed {
			steps = append(steps, step.key)
		} else {
			steps = append(steps, strconv.Itoa(step.index))
		}
	}
	slices.Reverse(steps)
	return steps
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
			return nil, fail(EvaluationFailed, "%s: %w", piece.code.written(), err).within(at.String())
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
		return nil, fail(CoercionFailed, "%s is %s, which strict %s coercion refuses%s", compiled.written(), absent, absent, hintFor(absent, compiled)).within(at.String())
	}
	return nil, nil
}

func hintFor(absent string, compiled expression) string {
	if absent != "undefined" {
		return ""
	}
	return compiled.undefinedHint()
}

func (r *renderer) evaluate(compiled expression, at *dataPath) (any, error) {
	session, err := r.engine.open()
	if err != nil {
		return nil, err
	}
	result, err := session.evaluate(compiled, r.args)
	if err != nil {
		return nil, fail(EvaluationFailed, "%s: %w", compiled.written(), err).within(at.String())
	}
	return result, nil
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
