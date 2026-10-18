package jaml

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
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
	plan := blueprint{Written: written, Null: configured.null, Undefined: configured.undefined}
	spec := evaluatorSpec{blueprint: plan, memoryLimit: configured.memoryLimitOrDefault()}
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
	if _, isTemplateError := err.(templateError); isTemplateError {
		return err
	}
	return fail(EvaluationFailed, "starting the evaluator: %w", err)
}

func (t *SecureTemplate) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error {
	if t == nil || t.slot == nil {
		return unmadeTemplate()
	}
	if t.lifetime.Err() != nil {
		return fail(Closed, "Eval after Close or after the template's context ended")
	}
	if ctx.Err() != nil {
		return fail(EvaluationFailed, "waiting for the evaluator: %w", cancelled(ctx))
	}
	select {
	case slot := <-t.slot:
		defer func() { t.slot <- slot }()
		err := t.core.eval(slot.renderer(ctx), evalCall{into: into, vars: vars, path: path})
		if err != nil && t.lifetime.Err() != nil {
			return fail(Closed, "closed during Eval")
		}
		return err
	case <-ctx.Done():
		return fail(EvaluationFailed, "waiting for the evaluator: %w", cancelled(ctx))
	case <-t.lifetime.Done():
		return fail(Closed, "closed while waiting for the evaluator")
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
	if _, isTemplateError := err.(templateError); isTemplateError {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("starting the evaluator: %w", err)
	}
	s.process = started
	return started, nil
}

func (s *evaluatorSlot) renderer(ctx context.Context) renderSite {
	return func(job rendering) ([]byte, error) {
		process, err := s.ready(ctx)
		if err != nil {
			return nil, err
		}
		return process.render(ctx, job)
	}
}
