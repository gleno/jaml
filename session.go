package jaml

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/dop251/goja"
)

const (
	maxCallStackSize = 4096
	maxUnwraps       = 16
)

var errInterrupted = errors.New("interrupted")

type sessionPlan struct {
	values  boundValues
	scripts []string
	pinned  pinned
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
		return nil, err
	}
	l.opened = opened
	return opened, nil
}

func (l *lazySession) warm() error {
	if len(l.plan.scripts) == 0 {
		return nil
	}
	_, err := l.open()
	return err
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
	var freezer goja.Callable
	if err := opened.enter(func(r realm) (err error) { freezer, err = r.intrinsicsFreezer(); return err }); err != nil {
		opened.close()
		return nil, fail(EvaluationFailed, "%w", err)
	}
	if err := opened.bind(plan.values); err != nil {
		opened.close()
		return nil, fail(EvaluationFailed, "binding values: %w", err)
	}
	if err := opened.run(plan.scripts, plan.values); err != nil {
		opened.close()
		return nil, err
	}
	if err := opened.freezeIntrinsics(freezer); err != nil {
		opened.close()
		return nil, fail(EvaluationFailed, "%w", err)
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

func (s *session) bind(values boundValues) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if err := s.enter(func(r realm) error { return r.bindValue(name, values[name]) }); err != nil {
			return err
		}
	}
	return nil
}

func (r realm) bindValue(name string, value datum) error {
	parsed, err := r.parse(goja.Undefined(), r.vm.ToValue(string(value.encoded)))
	if err != nil {
		return fmt.Errorf("value %q: %w", clip(name, keyExcerptLength), r.explain(err))
	}
	if err := r.deepFreeze(parsed); err != nil {
		return fmt.Errorf("value %q: %w", clip(name, keyExcerptLength), r.explain(err))
	}
	if err := r.vm.GlobalObject().DefineDataProperty(name, parsed, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
		return fmt.Errorf("value %q cannot be bound: %w", clip(name, keyExcerptLength), r.explain(err))
	}
	return nil
}

func (r realm) deepFreeze(value goja.Value) error {
	object, isObject := value.(*goja.Object)
	if !isObject {
		return nil
	}
	if _, err := r.freeze(goja.Undefined(), object); err != nil {
		return err
	}
	for _, key := range object.Keys() {
		if err := r.deepFreeze(object.Get(key)); err != nil {
			return err
		}
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

func (s *session) evaluate(compiled expression, args []datum) (any, error) {
	return s.attempt(func(r realm) (goja.Value, error) {
		settled, err := r.settle(compiled, args)
		return settled, r.explainWrite(compiled, err)
	})
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

func (r realm) settle(compiled expression, args []datum) (goja.Value, error) {
	value, err := r.vm.RunProgram(compiled.program)
	for calls := 0; err == nil; calls++ {
		function, isFunction := goja.AssertFunction(value)
		switch {
		case !isFunction:
			return value, nil
		case calls == maxUnwraps:
			return nil, fmt.Errorf("still a function after %d calls", maxUnwraps)
		case calls == 0:
			arguments, failed := r.arguments(args)
			if failed != nil {
				return nil, failed
			}
			value, err = function(r.vm.GlobalObject(), arguments...)
		default:
			value, err = function(r.vm.GlobalObject())
		}
	}
	return nil, err
}

func (r realm) arguments(args []datum) ([]goja.Value, error) {
	var values = make([]goja.Value, len(args))
	for index, arg := range args {
		parsed, err := r.parse(goja.Undefined(), r.vm.ToValue(string(arg.encoded)))
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index+1, r.explain(err))
		}
		values[index] = parsed
	}
	return values, nil
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
