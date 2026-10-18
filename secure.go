package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gleno/fault"
)

type SecureTemplate struct {
	core       *template
	spec       evaluatorSpec
	turn       sync.Mutex
	state      sync.Mutex
	evaluator  *evaluatorProcess
	closed     bool
	stopOnDone func() bool
}

func MakeSecureTemplate(ctx context.Context, written string, options ...Option) (*SecureTemplate, error) {
	if err := secureTemplatesSupported(); err != nil {
		return nil, err
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	if configured.memoryLimit == 0 {
		configured.memoryLimit = defaultMemoryLimit
	}
	core, err := makeTemplate(written, configured)
	if err != nil {
		return nil, err
	}
	spec := evaluatorSpec{sources: core.sources, memoryLimit: configured.memoryLimit}
	evaluator, err := startEvaluator(ctx, spec)
	if err != nil {
		return nil, fault.From(EvaluationFailed, fmt.Sprintf("starting the evaluator: %s", err))
	}
	secured := &SecureTemplate{core: core, spec: spec, evaluator: evaluator}
	secured.stopOnDone = context.AfterFunc(ctx, func() { _ = secured.Close() })
	return secured, nil
}

func (t *SecureTemplate) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error {
	t.turn.Lock()
	defer t.turn.Unlock()
	if t.isClosed() {
		return fault.From(Closed, "Eval after Close or after the template's context ended")
	}
	err := t.core.eval(evaluatorEngine{template: t, ctx: ctx}, evalCall{into: into, vars: vars, path: path})
	if err != nil && t.isClosed() {
		return fault.From(Closed, "closed during Eval")
	}
	return err
}

func (t *SecureTemplate) Close() error {
	t.stopOnDone()
	t.state.Lock()
	t.closed = true
	evaluator := t.evaluator
	t.evaluator = nil
	t.state.Unlock()
	if evaluator != nil {
		evaluator.kill()
	}
	return nil
}

func (t *SecureTemplate) isClosed() bool {
	t.state.Lock()
	defer t.state.Unlock()
	return t.closed
}

func (t *SecureTemplate) acquire(ctx context.Context) (*evaluatorProcess, error) {
	t.state.Lock()
	defer t.state.Unlock()
	if t.closed {
		return nil, errors.New("template closed")
	}
	if t.evaluator != nil && !t.evaluator.hasExited() {
		return t.evaluator, nil
	}
	started, err := startEvaluator(ctx, t.spec)
	if err != nil {
		return nil, err
	}
	t.evaluator = started
	return started, nil
}

type evaluatorEngine struct {
	template *SecureTemplate
	ctx      context.Context
}

func (e evaluatorEngine) open(vars encodedVars) (runtime, error) {
	evaluator, err := e.template.acquire(e.ctx)
	if err != nil {
		return nil, err
	}
	session := evaluatorSession{evaluator: evaluator, ctx: e.ctx}
	if _, err := session.call(request{Op: opOpen, Vars: vars}); err != nil {
		return nil, err
	}
	return session, nil
}

type evaluatorSession struct {
	evaluator *evaluatorProcess
	ctx       context.Context
}

func (s evaluatorSession) evaluate(compiled expression) (any, error) {
	answer, err := s.call(request{Op: opEval, ID: compiled.id})
	if err != nil {
		return nil, err
	}
	if answer.Undefined {
		return undefined{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(answer.Value))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("reading the evaluator's answer: %w", err)
	}
	return result, nil
}

func (s evaluatorSession) close() {
	if !s.evaluator.hasExited() {
		_, _ = s.call(request{Op: opClose})
	}
}

func (s evaluatorSession) call(next request) (response, error) {
	answer, err := s.evaluator.roundTrip(s.ctx, next)
	if err != nil {
		return response{}, err
	}
	if answer.Error != "" {
		return response{}, errors.New(answer.Error)
	}
	return answer, nil
}
