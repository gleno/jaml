package jaml

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

type _Renderer struct {
	compiled *_Compiled
	settings _ExpressionSettings
	engine   *_LazySession
	args     []_Datum
	skips    *_Shape
	depth    int
	skipped  [][]string
}

type _Place struct {
	path  *_DataPath
	skips *_ShapeCursor
	root  any
}

func (r *_Renderer) place(chosen _Subtree) _Place {
	var place = _Place{path: chosen.path, root: chosen.root}
	if r.skips != nil {
		place.skips = &_ShapeCursor{shape: r.skips}
	}
	return place
}

func (p _Place) key(key string) _Place {
	return _Place{path: p.path.atKey(key), skips: p.skips.key(key), root: p.root}
}

func (p _Place) item(index int) _Place {
	return _Place{path: p.path.atIndex(index), skips: p.skips.item(), root: p.root}
}

func (r *_Renderer) render(node any, place _Place) (any, error) {
	if place.skips.skips() {
		r.skipped = append(r.skipped, r.relative(place.path))
		return nil, nil
	}
	if place.root == nil {
		place.root = r.compiled.staticRoot(node)
	}
	if place.skips == nil {
		if encoded, spliced := r.compiled.spliced(place.root, node); spliced {
			return encoded, nil
		}
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
		var rendered = make([]any, len(typed))
		for index, item := range typed {
			value, err := r.render(item, place.item(index))
			if err != nil {
				return nil, err
			}
			rendered[index] = value
		}
		return rendered, nil
	case string:
		compiled, hasCode := r.compiled.texts[typed]
		if !hasCode {
			return typed, nil
		}
		produced, err := r.renderText(compiled, place.path)
		if err != nil {
			return nil, err
		}
		return produced, place.refuseCodeAtSkips(produced)
	default:
		return node, nil
	}
}

func (p _Place) refuseCodeAtSkips(produced any) error {
	switch {
	case p.skips == nil:
		return nil
	case p.skips.skips():
		if produced == nil {
			return nil
		}
		return fail(CoercionFailed, "code produced a value where Resolve expects a *jaml.Expression, which binds only to a document node").within(p.path.String())
	}
	switch typed := produced.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			if err := p.key(key).refuseCodeAtSkips(typed[key]); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := p.item(index).refuseCodeAtSkips(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *_Renderer) relative(path *_DataPath) []string {
	var steps []string
	for step := path; step.depth() > r.depth; step = step.parent {
		if step.keyed {
			steps = append(steps, step.key)
		} else {
			steps = append(steps, strconv.Itoa(step.index))
		}
	}
	slices.Reverse(steps)
	return steps
}

func (r *_Renderer) renderText(compiled _Text, path *_DataPath) (any, error) {
	if compiled.whole != nil {
		result, err := r.evaluate(*compiled.whole, path)
		if err != nil {
			return nil, err
		}
		return r.admit(result, *compiled.whole, path)
	}
	var rendered strings.Builder
	for _, piece := range compiled.segments {
		if piece.code == nil {
			rendered.WriteString(piece.literal)
			continue
		}
		result, err := r.evaluate(*piece.code, path)
		if err != nil {
			return nil, err
		}
		admitted, err := r.admit(result, *piece.code, path)
		if err != nil {
			return nil, err
		}
		written, err := stringify(admitted)
		if err != nil {
			return nil, failWith(EvaluationFailed, _Quoted{code: piece.code.written(), reason: err}).within(path.String())
		}
		rendered.WriteString(written)
	}
	return rendered.String(), nil
}

func (r *_Renderer) admit(result any, compiled _Expression, path *_DataPath) (any, error) {
	absent, coercion := "", Loose
	switch result.(type) {
	case nil:
		absent, coercion = "null", r.settings.null
	case _Undefined:
		absent, coercion = "undefined", r.settings.undefined
	default:
		return result, nil
	}
	if coercion == Strict {
		return nil, failWith(CoercionFailed, _Quoted{code: compiled.written(), reason: fmt.Errorf("result is %s, which strict %s coercion refuses%s", absent, absent, hintFor(absent, compiled))}).within(path.String())
	}
	return nil, nil
}

func hintFor(absent string, compiled _Expression) string {
	if absent != "undefined" {
		return ""
	}
	return compiled.undefinedHint()
}

func (r *_Renderer) evaluate(compiled _Expression, path *_DataPath) (any, error) {
	session, err := r.engine.open()
	if err != nil {
		return nil, err
	}
	result, err := session.evaluate(compiled, r.args)
	if err != nil {
		return nil, failWith(EvaluationFailed, _Quoted{code: compiled.written(), reason: err}).within(path.String())
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
