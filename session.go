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
	broken          error
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

func (s *session) bind(vars encodedVars) (err error) {
	defer s.breakOnEngineFailure(&err)
	parse := s.guardJSONParse()
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
	if err := json.Set("parse", guarded); err != nil {
		panic(err)
	}
	callable, _ := goja.AssertFunction(guarded)
	return callable
}

func (s *session) evaluate(compiled expression) (result any, err error) {
	if s.broken != nil {
		return nil, fmt.Errorf("an earlier expression broke the engine: %w", s.broken)
	}
	defer s.breakOnEngineFailure(&err)
	return s.run(compiled)
}

func (s *session) breakOnEngineFailure(err *error) {
	if failure := engineFailure(recover()); failure != nil {
		s.broken, *err = failure, failure
	}
}

func engineFailure(recovered any) error {
	switch cause := recovered.(type) {
	case nil:
		return nil
	case error:
		return fmt.Errorf("the engine failed: %w", cause)
	default:
		return fmt.Errorf("the engine failed: %v", cause)
	}
}

func (s *session) run(compiled expression) (any, error) {
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
	message, hasMessage := primitiveField(object, "message")
	if !hasMessage {
		return "threw [object " + object.ClassName() + "]"
	}
	name, hasName := primitiveField(object, "name")
	if !hasName {
		return message
	}
	return name + ": " + message
}

func primitiveField(object *goja.Object, key string) (string, bool) {
	field := object.Get(key)
	if field == nil || goja.IsUndefined(field) || goja.IsNull(field) {
		return "", false
	}
	if _, isObject := field.(*goja.Object); isObject {
		return "", false
	}
	return field.String(), true
}
