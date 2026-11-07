package jaml

import (
	"errors"
	"fmt"

	"github.com/dop251/goja"
)

type _Proxies struct {
	construct goja.Value
	handlers  *goja.Object
	reflect   *goja.Object
}

func captureProxies(vm *goja.Runtime) _Proxies {
	var handlers = vm.NewObject()
	if err := handlers.SetPrototype(nil); err != nil {
		panic(err)
	}
	return _Proxies{construct: vm.Get("Proxy"), handlers: handlers, reflect: vm.Get("Reflect").ToObject(vm)}
}

type _LazyObject struct {
	realm   _Realm
	number  int
	encoded _Datum
	target  *goja.Object
	pending map[string]_Datum
}

func (r _Realm) arguments(args []_Datum) ([]goja.Value, error) {
	var values = make([]goja.Value, len(args))
	for index, arg := range args {
		built, err := r.argument(arg, index+1)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index+1, r.explain(err))
		}
		values[index] = built
	}
	return values, nil
}

func (r _Realm) argument(arg _Datum, number int) (goja.Value, error) {
	if !arg.isObject() {
		return r.build(arg, _Mutable{})
	}
	var lazy = &_LazyObject{realm: r, number: number, encoded: arg, target: r.vm.NewObject()}
	return r.vm.New(r.proxies.construct, lazy.target, lazy.handler())
}

func (d _Datum) isObject() bool {
	var opening = &_ValueBuild{text: d.encoded}
	opening.space()
	return opening.next() == '{'
}

func (r _Realm) isLazyObject(object *goja.Object) bool {
	held, isProxy := object.Export().(goja.Proxy)
	if !isProxy {
		return false
	}
	var handler = held.Handler()
	return handler != nil && handler.Prototype() != nil && handler.Prototype().SameAs(r.proxies.handlers)
}

func (r _Realm) isForeignProxy(object *goja.Object) bool {
	return object.ExportType() == proxyType && !r.isLazyObject(object)
}

func (l *_LazyObject) handler() *goja.Object {
	var handler = l.realm.vm.NewObject()
	if err := handler.SetPrototype(l.realm.proxies.handlers); err != nil {
		panic(err)
	}
	for _, name := range []string{"get", "set", "defineProperty", "getOwnPropertyDescriptor"} {
		l.trap(handler, name, l.settle)
	}
	for _, name := range []string{"has", "ownKeys", "preventExtensions"} {
		l.trap(handler, name, func(goja.Value) { l.index() })
	}
	l.trap(handler, "deleteProperty", l.forget)
	return handler
}

func (l *_LazyObject) trap(handler *goja.Object, name string, before func(key goja.Value)) {
	var forward = method(l.realm.proxies.reflect, name)
	var trapped = _Native{name: name, call: func(call goja.FunctionCall) goja.Value {
		before(call.Argument(1))
		result, err := forward(goja.Undefined(), call.Arguments...)
		if err != nil {
			panic(err)
		}
		return result
	}}.on(l.realm.vm)
	if err := handler.DefineDataProperty(name, trapped, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_FALSE); err != nil {
		panic(err)
	}
}

func (l *_LazyObject) index() {
	if l.pending != nil {
		return
	}
	l.pending = map[string]_Datum{}
	var fields = &_ValueBuild{realm: l.realm, text: l.encoded.encoded}
	var err = fields.each(func(key string, value _Datum) error {
		l.pending[key] = value
		return l.target.DefineDataProperty(key, goja.Undefined(), goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_TRUE)
	})
	l.fail(err)
}

func (l *_LazyObject) settle(key goja.Value) {
	var name, value, pending = l.find(key)
	if !pending {
		return
	}
	built, err := l.realm.build(value, _Mutable{})
	if l.fail(err) {
		return
	}
	delete(l.pending, name)
	if err := l.target.DefineDataProperty(name, built, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
		panic(err)
	}
}

func (l *_LazyObject) forget(key goja.Value) {
	if name, _, pending := l.find(key); pending {
		delete(l.pending, name)
	}
}

func (l *_LazyObject) find(key goja.Value) (string, _Datum, bool) {
	l.index()
	written, isText := key.(goja.String)
	if !isText {
		return "", _Datum{}, false
	}
	var name = written.String()
	value, pending := l.pending[name]
	return name, value, pending && key.StrictEquals(l.realm.vm.ToValue(name))
}

func (l *_LazyObject) fail(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errInterrupted):
		return true
	}
	panic(fmt.Errorf("argument %d: %w", l.number, l.realm.explain(err)))
}
