package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	goruntime "runtime"

	"github.com/gleno/fault"
)

var (
	errClosed    = errors.New("closed")
	errAbandoned = errors.New("abandoned without Close")
)

type SecureTemplate struct {
	core     *template
	lifetime context.Context
	end      context.CancelCauseFunc
	slot     chan *evaluatorSlot
}

type evaluatorSlot struct {
	lifetime context.Context
	spec     evaluatorSpec
	process  *evaluatorProcess
}

func MakeSecureTemplate(ctx context.Context, written string, options ...Option) (*SecureTemplate, error) {
	if err := secureTemplatesSupported(); err != nil {
		return nil, err
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	core, err := makeTemplate(written, configured)
	if err != nil {
		return nil, err
	}
	lifetime, end := context.WithCancelCause(ctx)
	spec := evaluatorSpec{sources: core.sources, memoryLimit: configured.memoryLimitOrDefault()}
	first, err := spawnEvaluator(lifetime, lifetime, spec)
	if err != nil {
		end(err)
		if ctx.Err() != nil {
			err = cancelled(ctx)
		}
		return nil, startFailure(err)
	}
	slot := make(chan *evaluatorSlot, 1)
	slot <- &evaluatorSlot{lifetime: lifetime, spec: spec, process: first}
	secured := &SecureTemplate{core: core, lifetime: lifetime, end: end, slot: slot}
	goruntime.AddCleanup(secured, func(end context.CancelCauseFunc) { end(errAbandoned) }, end)
	return secured, nil
}

func startFailure(err error) error {
	if errors.Is(err, TemplateError) {
		return err
	}
	return fault.From(EvaluationFailed, fmt.Sprintf("starting the evaluator: %s", err))
}

func (t *SecureTemplate) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error {
	if t == nil || t.slot == nil {
		return unmadeTemplate()
	}
	if t.lifetime.Err() != nil {
		return fault.From(Closed, "Eval after Close or after the template's context ended")
	}
	select {
	case slot := <-t.slot:
		defer func() { t.slot <- slot }()
		err := t.core.eval(evaluatorEngine{slot: slot, ctx: ctx}, evalCall{into: into, vars: vars, path: path})
		if err != nil && t.lifetime.Err() != nil {
			return fault.From(Closed, "closed during Eval")
		}
		return err
	case <-ctx.Done():
		return fault.From(EvaluationFailed, fmt.Sprintf("waiting for the evaluator: %s", cancelled(ctx)))
	case <-t.lifetime.Done():
		return fault.From(Closed, "closed while waiting for the evaluator")
	}
}

func (t *SecureTemplate) Close() error {
	if t != nil && t.end != nil {
		t.end(errClosed)
	}
	return nil
}

func (s *evaluatorSlot) ready(ctx context.Context) (*evaluatorProcess, error) {
	if s.process.alive() {
		return s.process, nil
	}
	started, err := spawnEvaluator(ctx, s.lifetime, s.spec)
	if err != nil {
		return nil, fmt.Errorf("starting the evaluator: %w", err)
	}
	s.process = started
	return started, nil
}

type evaluatorEngine struct {
	slot *evaluatorSlot
	ctx  context.Context
}

func (e evaluatorEngine) open(vars encodedVars) (runtime, error) {
	process, err := e.slot.ready(e.ctx)
	if err != nil {
		return nil, err
	}
	session := evaluatorSession{process: process, ctx: e.ctx}
	if _, err := session.call(request{Op: opOpen, Vars: vars}); err != nil {
		return nil, err
	}
	return session, nil
}

type evaluatorSession struct {
	process *evaluatorProcess
	ctx     context.Context
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

func (s evaluatorSession) call(next request) (response, error) {
	answer, err := s.process.roundTrip(s.ctx, next)
	if err != nil {
		return response{}, err
	}
	if answer.Error != "" {
		return response{}, errors.New(answer.Error)
	}
	return answer, nil
}
