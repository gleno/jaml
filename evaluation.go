package jaml

import (
	"fmt"
	"strings"

	"github.com/gleno/fault"
)

const sourcePreviewLength = 60

type evaluation struct {
	settings settings
	vars     map[string]any
	session  *session
}

func openEvaluation(configured settings, vars map[string]any) *evaluation {
	return &evaluation{settings: configured, vars: vars}
}

func (e *evaluation) close() {
	if e.session != nil {
		e.session.close()
	}
}

func (e *evaluation) evaluate(compiled expression, at location) (value, error) {
	if e.session == nil {
		opened, err := openSession(e.vars, e.settings.timeout)
		if err != nil {
			return nil, fault.From(EvaluationFailed, fmt.Sprintf("%s: binding vars: %s", at, err))
		}
		e.session = opened
	}
	result, err := e.session.evaluate(compiled)
	if err != nil {
		return nil, fault.From(EvaluationFailed, fmt.Sprintf("%s: %s: %s", at, compiled.written(), err))
	}
	return result, nil
}

func (e *evaluation) resolve(v value, at location) (value, error) {
	switch typed := v.(type) {
	case expressionValue:
		return e.evaluate(typed.expression, at)
	case interpolationValue:
		text, err := e.interpolate(typed, at)
		return stringValue(text), err
	default:
		return v, nil
	}
}

func (e *evaluation) interpolate(segments interpolationValue, at location) (string, error) {
	var text strings.Builder
	for _, part := range segments {
		if part.expression == nil {
			text.WriteString(part.text)
			continue
		}
		piece, err := e.interpolatePart(*part.expression, at)
		if err != nil {
			return "", err
		}
		text.WriteString(piece)
	}
	return text.String(), nil
}

func (e *evaluation) interpolatePart(compiled expression, at location) (string, error) {
	result, err := e.evaluate(compiled, at)
	if err != nil {
		return "", err
	}
	switch result.(type) {
	case nullValue, undefinedValue:
		if e.coercionOf(result) == Strict {
			return "", fault.From(CoercionFailed, fmt.Sprintf("%s: %s is %s, which strict %s coercion refuses to interpolate", at, compiled.written(), result.kind(), result.kind()))
		}
		return "", nil
	default:
		return e.stringify(result, at)
	}
}

func (e *evaluation) coercionOf(absent value) Coercion {
	if _, isUndefined := absent.(undefinedValue); isUndefined {
		return e.settings.undefined
	}
	return e.settings.null
}

func preview(source string) string {
	flattened := strings.Join(strings.Fields(source), " ")
	if len(flattened) <= sourcePreviewLength {
		return flattened
	}
	return flattened[:sourcePreviewLength] + "…"
}
