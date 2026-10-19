package jaml

import (
	"context"
)

type standalone struct {
	compiled expression
	settings settings
}

func compileStandalone(source string, configured settings) (*standalone, error) {
	compiled, err := compileBare(source, configured)
	if err != nil {
		return nil, fail(InvalidTemplate, "%s: %w", bare.around(source), err)
	}
	return &standalone{compiled: compiled, settings: configured}, nil
}

func compileBare(source string, configured settings) (expression, error) {
	parsed, err := parseCode(source)
	if err != nil {
		return expression{}, err
	}
	if configured.function {
		if err := parsed.refuseUnlessFunction(); err != nil {
			return expression{}, err
		}
	}
	return parsed.compile(bare)
}

func (s *standalone) prepare(request work) (prepared, error) {
	plan := sessionPlan{vars: request.Vars, pinned: request.Pinned}
	return prepared{at: (*dataPath)(nil).String(), run: func(ctx context.Context) ([]byte, error) {
		engine := &lazySession{ctx: ctx, plan: plan}
		defer engine.close()
		renderer := &renderer{settings: s.settings, engine: engine}
		result, err := s.produce(renderer, request.Call)
		if err != nil {
			return nil, err
		}
		return encodeResult(result, nil)
	}}, nil
}

func (s *standalone) produce(renderer *renderer, call *callArgs) (any, error) {
	if call == nil {
		return renderer.renderText(text{whole: &s.compiled}, nil)
	}
	return renderer.call(s.compiled, call.Args)
}
