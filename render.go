package jaml

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gleno/fault"
)

const sourcePreviewLength = 60

type renderer struct {
	template *template
	engine   engine
	vars     map[string]any
	runtime  runtime
}

func (r *renderer) close() {
	if r.runtime != nil {
		r.runtime.close()
	}
}

func (r *renderer) render(node any, at string) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		rendered := make(map[string]any, len(typed))
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			value, err := r.render(typed[key], at+keyStep(key))
			if err != nil {
				return nil, err
			}
			rendered[key] = value
		}
		return rendered, nil
	case []any:
		rendered := make([]any, len(typed))
		for index, item := range typed {
			value, err := r.render(item, at+indexStep(index))
			if err != nil {
				return nil, err
			}
			rendered[index] = value
		}
		return rendered, nil
	case string:
		compiled, hasCode := r.template.texts[typed]
		if !hasCode {
			return typed, nil
		}
		return r.renderText(compiled, at)
	default:
		return node, nil
	}
}

func (r *renderer) renderText(compiled text, at string) (any, error) {
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
			return nil, fault.From(EvaluationFailed, fmt.Sprintf("%s: %s: %s", at, piece.code.written(), err))
		}
		rendered.WriteString(written)
	}
	return rendered.String(), nil
}

func (r *renderer) admit(result any, compiled expression, at string) (any, error) {
	absent, coercion := "", Loose
	switch result.(type) {
	case nil:
		absent, coercion = "null", r.template.settings.null
	case undefined:
		absent, coercion = "undefined", r.template.settings.undefined
	default:
		return result, nil
	}
	if coercion == Strict {
		return nil, fault.From(CoercionFailed, fmt.Sprintf("%s: %s is %s, which strict %s coercion refuses", at, compiled.written(), absent, absent))
	}
	return nil, nil
}

func (r *renderer) evaluate(compiled expression, at string) (any, error) {
	if r.runtime == nil {
		opened, err := r.openRuntime()
		if err != nil {
			return nil, fault.From(EvaluationFailed, fmt.Sprintf("%s: binding vars: %s", at, err))
		}
		r.runtime = opened
	}
	result, err := r.runtime.evaluate(compiled)
	if err != nil {
		return nil, fault.From(EvaluationFailed, fmt.Sprintf("%s: %s: %s", at, compiled.written(), err))
	}
	return result, nil
}

func (r *renderer) openRuntime() (runtime, error) {
	encoded, err := encodeVars(r.vars)
	if err != nil {
		return nil, err
	}
	return r.engine.open(encoded)
}

func stringify(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	default:
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	}
}

func preview(source string) string {
	flattened := strings.Join(strings.Fields(source), " ")
	if len(flattened) <= sourcePreviewLength {
		return flattened
	}
	return flattened[:sourcePreviewLength] + "…"
}
