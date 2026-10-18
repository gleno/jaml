package jaml

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/dop251/goja"
)

const maxCallStackSize = 4096

type session struct {
	vm              *goja.Runtime
	objectPrototype *goja.Object
}

func openSession(vars encodedVars) (*session, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStackSize)
	opened := &session{vm: vm, objectPrototype: vm.Get("Object").ToObject(vm).Get("prototype").ToObject(vm)}
	if err := opened.bind(vars); err != nil {
		return nil, err
	}
	return opened, nil
}

func (s *session) close() {}

func (s *session) bind(vars encodedVars) error {
	parse, _ := goja.AssertFunction(s.vm.Get("JSON").ToObject(s.vm).Get("parse"))
	global := s.vm.GlobalObject()
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		parsed, err := parse(goja.Undefined(), s.vm.ToValue(string(vars[name])))
		if err != nil {
			return fmt.Errorf("var %q: %w", name, err)
		}
		if err := global.DefineDataProperty(name, parsed, goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
			return fmt.Errorf("var %q cannot be bound: %w", name, err)
		}
	}
	return nil
}

func (s *session) evaluate(compiled expression) (any, error) {
	wrapper, err := s.vm.RunProgram(compiled.program)
	if err != nil {
		return nil, s.describe(err)
	}
	call, isCallable := goja.AssertFunction(wrapper)
	if !isCallable {
		return nil, errors.New("the expression wrapper is not callable")
	}
	returned, err := call(goja.Undefined())
	if err != nil {
		return nil, s.describe(err)
	}
	var result any
	var readErr error
	if err := s.guarded(func() { result, readErr = s.read(returned) }); err != nil {
		return nil, s.describe(err)
	}
	return result, readErr
}

func (s *session) guarded(run func()) error {
	native, _ := goja.AssertFunction(s.vm.ToValue(func(goja.FunctionCall) goja.Value {
		run()
		return goja.Undefined()
	}))
	_, err := native(goja.Undefined())
	return err
}

func (s *session) read(returned goja.Value) (any, error) {
	reader := resultReader{objectPrototype: s.objectPrototype, ancestors: map[*goja.Object]bool{}}
	return reader.read(returned, "")
}

func (s *session) describe(err error) error {
	var overflowed *goja.StackOverflowError
	if errors.As(err, &overflowed) {
		return fmt.Errorf("exceeded the maximum call depth of %d", maxCallStackSize)
	}
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return errors.New(s.thrownMessage(exception))
	}
	return err
}

func (s *session) thrownMessage(exception *goja.Exception) string {
	var message string
	if err := s.guarded(func() { message = describeThrown(exception.Value()) }); err != nil {
		return "threw a value whose message cannot be read"
	}
	return message
}

func describeThrown(thrown goja.Value) string {
	object, isObject := thrown.(*goja.Object)
	if !isObject {
		return "threw " + thrown.String()
	}
	message := object.Get("message")
	if message == nil || goja.IsUndefined(message) || goja.IsNull(message) {
		return "threw " + object.String()
	}
	name := object.Get("name")
	if name == nil || goja.IsUndefined(name) || goja.IsNull(name) {
		return message.String()
	}
	return name.String() + ": " + message.String()
}
