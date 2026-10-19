package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/dop251/goja"
)

const maxCallStackSize = 4096

var errInterrupted = errors.New("interrupted")

type sessionPlan struct {
	vars   encodedVars
	pinned pinned
}

type lazySession struct {
	ctx    context.Context
	plan   sessionPlan
	opened *session
}

func (l *lazySession) open() (*session, error) {
	if l.opened != nil {
		return l.opened, nil
	}
	opened, err := openSession(l.ctx, l.plan)
	if err != nil {
		return nil, fail(EvaluationFailed, "binding vars: %w", err)
	}
	l.opened = opened
	return opened, nil
}

func (l *lazySession) close() {
	if l.opened != nil {
		l.opened.close()
	}
}

type session struct {
	ctx     context.Context
	realm   realm
	broken  error
	release func() bool
}

func openSession(ctx context.Context, plan sessionPlan) (*session, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStackSize)
	vm.SetTimeSource(plan.pinned.now)
	vm.SetRandSource(plan.pinned.random())
	opened := &session{ctx: ctx, realm: captureRealm(vm)}
	opened.release = context.AfterFunc(ctx, func() { vm.Interrupt(errInterrupted) })
	if err := opened.bind(plan.vars); err != nil {
		opened.close()
		return nil, err
	}
	return opened, nil
}

func (s *session) close() {
	s.release()
}

func (s *session) enter(step func(realm) error) (err error) {
	if halted := s.halted(); halted != nil {
		return halted
	}
	defer s.breakOnEngineFailure(&err)
	var stepped error
	entered := s.realm.native(func() {
		if exception := s.realm.vm.Try(func() { stepped = step(s.realm) }); exception != nil {
			stepped = s.realm.explain(exception)
		}
	})
	if halted := s.halted(); halted != nil {
		return halted
	}
	if entered != nil {
		return uncatchable(entered)
	}
	return stepped
}

func (s *session) halted() error {
	switch {
	case s.broken != nil:
		return fmt.Errorf("an earlier expression broke the engine: %w", s.broken)
	case s.ctx.Err() != nil:
		return cancelled(s.ctx)
	}
	return nil
}

func (s *session) bind(vars encodedVars) error {
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := s.enter(func(r realm) error { return r.bindVar(name, vars[name]) }); err != nil {
			return err
		}
	}
	return nil
}

func (r realm) bindVar(name string, value json.RawMessage) error {
	parsed, err := r.parse(goja.Undefined(), r.vm.ToValue(string(value)))
	if err != nil {
		return fmt.Errorf("var %q: %w", clip(name, keyExcerptLength), r.explain(err))
	}
	if err := r.vm.GlobalObject().DefineDataProperty(name, parsed, goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
		return fmt.Errorf("var %q cannot be bound: %w", clip(name, keyExcerptLength), r.explain(err))
	}
	return nil
}

func guardJSONParse(vm *goja.Runtime) goja.Callable {
	json := vm.Get("JSON").ToObject(vm)
	parse, _ := goja.AssertFunction(json.Get("parse"))
	rangeError := vm.Get("RangeError")
	guarded := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).ToString()
		if jsonDepth([]byte(text.String())) > maxDataDepth {
			refusal, err := vm.New(rangeError, vm.ToValue(fmt.Sprintf("JSON text nests deeper than %d", maxDataDepth)))
			if err != nil {
				panic(err)
			}
			panic(refusal)
		}
		parsed, err := parse(call.This, append([]goja.Value{text}, call.Arguments[min(1, len(call.Arguments)):]...)...)
		if err != nil {
			panic(err)
		}
		return parsed
	})
	for key, value := range map[string]any{"name": "parse", "length": 2} {
		if err := guarded.ToObject(vm).DefineDataProperty(key, vm.ToValue(value), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE); err != nil {
			panic(err)
		}
	}
	if err := json.Set("parse", guarded); err != nil {
		panic(err)
	}
	callable, _ := goja.AssertFunction(guarded)
	return callable
}

func (s *session) evaluate(compiled expression) (any, error) {
	return s.attempt(func(r realm) (goja.Value, error) { return r.vm.RunProgram(compiled.program) })
}

func (s *session) call(compiled expression, args []json.RawMessage) (any, error) {
	return s.attempt(func(r realm) (goja.Value, error) { return r.invoke(compiled, args) })
}

func (s *session) attempt(produce func(realm) (goja.Value, error)) (any, error) {
	var result any
	err := s.enter(func(r realm) error {
		returned, err := produce(r)
		if err != nil {
			return r.explain(err)
		}
		result, err = readResult(r, returned)
		return err
	})
	return result, err
}

func (r realm) invoke(compiled expression, args []json.RawMessage) (goja.Value, error) {
	returned, err := r.vm.RunProgram(compiled.program)
	if err != nil {
		return nil, err
	}
	function, isFunction := goja.AssertFunction(returned)
	if !isFunction {
		return nil, fmt.Errorf("evaluates to a value of type %s, not a function", typeOf(returned))
	}
	values := make([]goja.Value, len(args))
	for index, arg := range args {
		parsed, err := r.parse(goja.Undefined(), r.vm.ToValue(string(arg)))
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index+1, r.explain(err))
		}
		values[index] = parsed
	}
	return function(goja.Undefined(), values...)
}

func (s *session) breakOnEngineFailure(err *error) {
	if failure := engineFailure(recover()); failure != nil {
		s.broken, *err = failure, failure
	}
}

func engineFailure(recovered any) error {
	cause, isError := recovered.(error)
	switch {
	case recovered == nil:
		return nil
	case isEngineValue(recovered) || isError && holdsEngineValue(cause):
		return fmt.Errorf("the engine failed with a %T", recovered)
	case isError:
		return fmt.Errorf("the engine failed: %w", cause)
	default:
		return fmt.Errorf("the engine failed: %v", recovered)
	}
}
