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
	ctx     context.Context
	plan    sessionPlan
	opened  *session
	release func() bool
}

func (l *lazySession) open() (*session, error) {
	if l.opened != nil {
		return l.opened, nil
	}
	if l.ctx.Err() != nil {
		return nil, fail(EvaluationFailed, "%w", cancelled(l.ctx))
	}
	opened, err := openSession(l.plan)
	if err != nil {
		return nil, fail(EvaluationFailed, "binding vars: %w", err)
	}
	l.opened = opened
	l.release = context.AfterFunc(l.ctx, func() { opened.vm.Interrupt(errInterrupted) })
	return opened, nil
}

func (l *lazySession) close() {
	if l.release != nil {
		l.release()
	}
}

type session struct {
	vm     *goja.Runtime
	realm  realm
	parse  goja.Callable
	broken error
}

func openSession(plan sessionPlan) (*session, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStackSize)
	vm.SetTimeSource(plan.pinned.now)
	vm.SetRandSource(plan.pinned.random())
	opened := &session{vm: vm, realm: captureRealm(vm)}
	if err := opened.bind(plan.vars); err != nil {
		return nil, err
	}
	return opened, nil
}

func (s *session) bind(vars encodedVars) (err error) {
	defer s.breakOnEngineFailure(&err)
	s.parse = s.guardJSONParse()
	global := s.vm.GlobalObject()
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		parsed, err := s.parse(goja.Undefined(), s.vm.ToValue(string(vars[name])))
		if err != nil {
			return fmt.Errorf("var %q: %w", clip(name, keyExcerptLength), s.realm.explain(err))
		}
		if err := global.DefineDataProperty(name, parsed, goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
			return fmt.Errorf("var %q cannot be bound: %w", clip(name, keyExcerptLength), s.realm.explain(err))
		}
	}
	return nil
}

func (s *session) guardJSONParse() goja.Callable {
	json := s.vm.Get("JSON").ToObject(s.vm)
	parse, _ := goja.AssertFunction(json.Get("parse"))
	rangeError := s.vm.Get("RangeError")
	guarded := s.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).ToString()
		if jsonDepth([]byte(text.String())) > maxDataDepth {
			refusal, err := s.vm.New(rangeError, s.vm.ToValue(fmt.Sprintf("JSON text nests deeper than %d", maxDataDepth)))
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
		if err := guarded.ToObject(s.vm).DefineDataProperty(key, s.vm.ToValue(value), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE); err != nil {
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
	return s.attempt(func() (goja.Value, error) { return s.vm.RunProgram(compiled.program) })
}

func (s *session) call(compiled expression, args []json.RawMessage) (any, error) {
	return s.attempt(func() (goja.Value, error) { return s.invoke(compiled, args) })
}

func (s *session) attempt(produce func() (goja.Value, error)) (result any, err error) {
	if s.broken != nil {
		return nil, fmt.Errorf("an earlier expression broke the engine: %w", s.broken)
	}
	defer s.breakOnEngineFailure(&err)
	returned, err := produce()
	if err != nil {
		return nil, s.realm.explain(err)
	}
	var readErr error
	if err := s.realm.guarded(func() { result, readErr = readResult(s.realm, returned) }); err != nil {
		return nil, s.realm.explain(err)
	}
	return result, readErr
}

func (s *session) invoke(compiled expression, args []json.RawMessage) (goja.Value, error) {
	returned, err := s.vm.RunProgram(compiled.program)
	if err != nil {
		return nil, err
	}
	function, isFunction := goja.AssertFunction(returned)
	if !isFunction {
		return nil, fmt.Errorf("evaluates to a value of type %s, not a function", typeOf(returned))
	}
	values := make([]goja.Value, len(args))
	for index, arg := range args {
		parsed, err := s.parse(goja.Undefined(), s.vm.ToValue(string(arg)))
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index+1, s.realm.explain(err))
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
