package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	goruntime "runtime"
)

var (
	errSandboxEnded = errors.New("the sandbox's context ended")
	errUnreachable  = errors.New("dropped without its context ending")
	errLifetimeOver = errors.New("its context ended")
)

type engine interface {
	build(ctx context.Context, plan blueprint) (unit, error)
}

type unit interface {
	serve(ctx context.Context, request work) outcome
	forget()
}

type Sandbox struct {
	lifetime context.Context
	engine   engine
}

type Template struct {
	held *handle
}

type Expression struct {
	held *handle
}

type handle struct {
	lifetime context.Context
	end      context.CancelCauseFunc
	unit     unit
}

func MakeSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error) {
	if err := isolationSupported(); err != nil {
		return nil, err
	}
	configured, err := configureSandbox(options)
	if err != nil {
		return nil, err
	}
	isolated, err := startIsolated(ctx, configured)
	if err != nil {
		return nil, err
	}
	return &Sandbox{lifetime: ctx, engine: isolated}, nil
}

func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error) {
	configured, err := configureSandbox(options)
	if err != nil {
		return nil, err
	}
	if configured.memoryLimit != 0 {
		return nil, fail(InvalidTemplate, "a trusted sandbox has no memory limit; use MakeSandbox")
	}
	return &Sandbox{lifetime: ctx, engine: newTrusted(configured)}, nil
}

func (s *Sandbox) MakeTemplate(ctx context.Context, text string, options ...Option) (*Template, error) {
	if s == nil || s.engine == nil {
		return nil, unmadeSandbox()
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	held, err := s.make(ctx, blueprint{Written: text, Null: configured.null, Undefined: configured.undefined})
	if err != nil {
		return nil, err
	}
	return &Template{held: held}, nil
}

func (s *Sandbox) Compile(ctx context.Context, source string, options ...Option) (*Expression, error) {
	if s == nil || s.engine == nil {
		return nil, unmadeSandbox()
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	held, err := s.make(ctx, blueprint{Written: source, Null: configured.null, Undefined: configured.undefined, Expression: true})
	if err != nil {
		return nil, err
	}
	return &Expression{held: held}, nil
}

func (s *Sandbox) make(ctx context.Context, plan blueprint) (*handle, error) {
	if s.lifetime.Err() != nil {
		return nil, fail(Closed, "the sandbox's context has ended")
	}
	if ctx.Err() != nil {
		return nil, fail(EvaluationFailed, "building: %w", cancelled(ctx))
	}
	lifetime, end := context.WithCancelCause(ctx)
	unfollow := context.AfterFunc(s.lifetime, func() { end(errSandboxEnded) })
	built, err := s.engine.build(lifetime, plan)
	if failure := s.buildFailure(ctx, err); failure != nil {
		unfollow()
		end(failure)
		return nil, failure
	}
	context.AfterFunc(lifetime, func() {
		unfollow()
		built.forget()
	})
	held := &handle{lifetime: lifetime, end: end, unit: built}
	goruntime.AddCleanup(held, func(end context.CancelCauseFunc) { end(errUnreachable) }, end)
	return held, nil
}

func (s *Sandbox) buildFailure(ctx context.Context, err error) error {
	switch {
	case s.lifetime.Err() != nil:
		return fail(Closed, "the sandbox's context ended while building")
	case err != nil && ctx.Err() != nil:
		return fail(EvaluationFailed, "building: %w", cancelled(ctx))
	case err != nil:
		return located("", err)
	}
	return nil
}

func unmadeSandbox() error {
	return fail(InvalidTemplate, "a sandbox not made by MakeSandbox or MakeTrustedSandbox")
}

func (t *Template) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error {
	if t == nil || t.held == nil {
		return fail(InvalidTemplate, "Eval on a template not made by Sandbox.MakeTemplate")
	}
	if err := t.held.admit(ctx); err != nil {
		return err
	}
	if err := checkTarget("Eval", into); err != nil {
		return err
	}
	encoded, err := encodeVars(vars)
	if err != nil {
		return fail(EvaluationFailed, "binding vars: %w", err)
	}
	return t.held.answer(ctx, into, work{Path: path, Vars: encoded})
}

func (e *Expression) Eval(ctx context.Context, into any, vars map[string]any) error {
	if e == nil || e.held == nil {
		return fail(InvalidTemplate, "Eval on an expression not made by Sandbox.Compile")
	}
	if err := e.held.admit(ctx); err != nil {
		return err
	}
	if err := checkTarget("Eval", into); err != nil {
		return err
	}
	encoded, err := encodeVars(vars)
	if err != nil {
		return fail(EvaluationFailed, "binding vars: %w", err)
	}
	return e.held.answer(ctx, into, work{Vars: encoded})
}

func (e *Expression) Call(ctx context.Context, into any, args ...any) error {
	if e == nil || e.held == nil {
		return fail(InvalidTemplate, "Call on an expression not made by Sandbox.Compile")
	}
	if err := e.held.admit(ctx); err != nil {
		return err
	}
	if err := checkTarget("Call", into); err != nil {
		return err
	}
	encoded, err := encodeArguments(args)
	if err != nil {
		return fail(EvaluationFailed, "binding arguments: %w", err)
	}
	return e.held.answer(ctx, into, work{Call: &callArgs{Args: encoded}})
}

func checkTarget(method string, into any) error {
	target := reflect.ValueOf(into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fail(InvalidTarget, "%s needs a non-nil pointer, got %s", method, describeTarget(into))
	}
	return nil
}

func (h *handle) admit(ctx context.Context) error {
	if h.lifetime.Err() != nil {
		return fail(Closed, "its context or its sandbox's context has ended")
	}
	if ctx.Err() != nil {
		return fail(EvaluationFailed, "%w", cancelled(ctx))
	}
	return nil
}

func (h *handle) answer(ctx context.Context, into any, request work) error {
	defer goruntime.KeepAlive(h)
	request.Pinned = pin(ctx)
	job, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	unbind := context.AfterFunc(h.lifetime, func() { stop(errLifetimeOver) })
	defer unbind()
	result := h.unit.serve(job, request)
	switch {
	case h.lifetime.Err() != nil:
		return fail(Closed, "its context or its sandbox's context ended while it ran")
	case ctx.Err() != nil:
		return located(result.at, cancelled(ctx))
	case result.err != nil:
		return located(result.at, result.err)
	}
	if err := json.Unmarshal(result.data, into); err != nil {
		return fail(CoercionFailed, "%s: %w", blame(result.at, result.data, err), err)
	}
	return nil
}
