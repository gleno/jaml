package jaml

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/dop251/goja"
)

const (
	unnamedFunctionsFile = "jaml:intrinsics"
	readOnlyRefusal      = "cannot change a read-only value: values from WithValues and the built-ins are frozen, so change a copy instead, such as items.toSorted(), [...items].reverse() or { ...item, key: value }"
	nameRefusal          = "cannot change a read-only value: a name from WithValues cannot be assigned, so change the value in place or keep the new one under another name"
)

type _Protection interface {
	_Closing
	freeze(r _Realm, survey _Survey) error
	refusal() string
}

func (r _Runtime) protect(vm *goja.Runtime) (_Protection, error) {
	if r.builtins != frozenBuiltins {
		return _Mutable{}, nil
	}
	intrinsics, err := captureIntrinsics(vm)
	if err != nil {
		return nil, fmt.Errorf("finding the built-ins: %w", err)
	}
	return _Frozen{intrinsics: intrinsics}, nil
}

type _Mutable struct{}

func (_Mutable) freeze(_Realm, _Survey) error {
	return nil
}

func (_Mutable) settle(_Realm, *goja.Object) error {
	return nil
}

func (_Mutable) refusal() string {
	return nameRefusal
}

type _Frozen struct {
	intrinsics *_Intrinsics
}

type _Intrinsics struct {
	ownKeys       goja.Callable
	describe      goja.Callable
	define        goja.Callable
	originalRoots []goja.Value
	homes         []_Home
}

type _Home struct {
	object goja.Value
	keys   []string
}

type _Overridable struct {
	global string
	keys   []string
}

var (
	errorOverridables = []string{"name", "message"}
	overridables      = []_Overridable{
		{global: "Error", keys: errorOverridables},
		{global: "EvalError", keys: errorOverridables},
		{global: "RangeError", keys: errorOverridables},
		{global: "ReferenceError", keys: errorOverridables},
		{global: "SyntaxError", keys: errorOverridables},
		{global: "TypeError", keys: errorOverridables},
		{global: "URIError", keys: errorOverridables},
		{global: "Object", keys: []string{"toString", "valueOf", "constructor", "hasOwnProperty", "toLocaleString", "isPrototypeOf", "propertyIsEnumerable"}},
		{global: "Function", keys: []string{"toString", "name", "constructor"}},
		{global: "Array", keys: []string{"constructor", "toString", "push"}},
		{global: "Promise", keys: []string{"constructor"}},
		{global: "Symbol", keys: []string{"constructor"}},
	}
	typedArrayOverridables = []string{"constructor", "toString", "push"}
)

var unnamedFunctions = sync.OnceValue(func() *goja.Program {
	return goja.MustCompile(unnamedFunctionsFile, "[function* () {}, async function () {}]", true)
})

func captureIntrinsics(vm *goja.Runtime) (*_Intrinsics, error) {
	var global = vm.GlobalObject()
	var object = global.Get("Object").ToObject(vm)
	var reflect = global.Get("Reflect").ToObject(vm)
	unnamed, err := unnamedIntrinsics(vm)
	if err != nil {
		return nil, err
	}
	var typedArray = global.Get("Uint8Array").ToObject(vm).Prototype()
	var homes = []_Home{{object: typedArray.Get("prototype"), keys: typedArrayOverridables}}
	for _, overridable := range overridables {
		var home goja.Value = goja.Null()
		if named, isObject := global.Get(overridable.global).(*goja.Object); isObject {
			home = named.Get("prototype")
		}
		homes = append(homes, _Home{object: home, keys: overridable.keys})
	}
	globals, err := pristineGlobals()
	if err != nil {
		return nil, err
	}
	// Capture before scripts can replace globals or sever constructor backlinks.
	// The engine still uses these originals for literals, boxing and exceptions.
	var roots = append(unnamed, typedArray)
	for _, key := range globals {
		roots = append(roots, valueAt(global, key))
	}
	return &_Intrinsics{
		ownKeys:       method(reflect, "ownKeys"),
		describe:      method(object, "getOwnPropertyDescriptor"),
		define:        method(reflect, "defineProperty"),
		originalRoots: roots,
		homes:         homes,
	}, nil
}

func method(home *goja.Object, name string) goja.Callable {
	called, _ := goja.AssertFunction(home.Get(name))
	return called
}

func unnamedIntrinsics(vm *goja.Runtime) ([]goja.Value, error) {
	functions, err := vm.RunProgram(unnamedFunctions())
	if err != nil {
		return nil, err
	}
	var global = vm.GlobalObject()
	var generator = functions.ToObject(vm).Get("0")
	var unnamed = []goja.Value{
		prototypeOf(vm, generator),
		prototypeOf(vm, generator.ToObject(vm).Get("prototype")),
		prototypeOf(vm, functions.ToObject(vm).Get("1")),
	}
	var iterables = []goja.Value{vm.NewArray(), vm.ToValue("")}
	for _, name := range []string{"Map", "Set"} {
		made, err := vm.New(global.Get(name))
		if err != nil {
			return nil, err
		}
		iterables = append(iterables, made)
	}
	for _, iterable := range iterables {
		iterate, _ := goja.AssertFunction(iterable.ToObject(vm).GetSymbol(goja.SymIterator))
		iterator, err := iterate(iterable)
		if err != nil {
			return nil, err
		}
		unnamed = append(unnamed, prototypeOf(vm, iterator))
	}
	pattern, err := vm.New(global.Get("RegExp"), vm.ToValue("a"), vm.ToValue("g"))
	if err != nil {
		return nil, err
	}
	var text = vm.ToValue("a")
	matches, err := method(text.ToObject(vm), "matchAll")(text, pattern)
	if err != nil {
		return nil, err
	}
	return append(unnamed, prototypeOf(vm, matches)), nil
}

func prototypeOf(vm *goja.Runtime, value goja.Value) goja.Value {
	if prototype := value.ToObject(vm).Prototype(); prototype != nil {
		return prototype
	}
	return goja.Null()
}

func (_Frozen) settle(r _Realm, object *goja.Object) error {
	_, err := r.freeze(goja.Undefined(), object)
	return err
}

func (_Frozen) refusal() string {
	return readOnlyRefusal
}

type _Survey func(freezing _Freezing, roots []goja.Value) ([]*goja.Object, error)

func (p _SessionPlan) survey() _Survey {
	if len(p.scripts) > 0 {
		return walkTheBuiltIns
	}
	return replayTheChart
}

func (f _Frozen) freeze(r _Realm, survey _Survey) error {
	var freezing = _Freezing{realm: r, intrinsics: f.intrinsics}
	if err := freezing.settle(survey); err != nil {
		return fmt.Errorf("freezing the built-ins: %w", r.explain(err))
	}
	return nil
}

type _Freezing struct {
	realm      _Realm
	intrinsics *_Intrinsics
}

func (z _Freezing) settle(survey _Survey) error {
	roots, err := z.tame()
	if err != nil {
		return err
	}
	loose, err := z.looseAccessors()
	if err != nil {
		return err
	}
	builtIns, err := survey(z, roots)
	if err != nil {
		return err
	}
	if len(loose) == 0 {
		return z.freezeAll(builtIns)
	}
	var seen = make(map[*goja.Object]int, len(builtIns))
	for index, object := range builtIns {
		seen[object] = index
	}
	reached, err := z.walk(loose, seen)
	if err != nil {
		return err
	}
	return z.freezeAll(append(builtIns, objectsOf(reached)...))
}

func (z _Freezing) tame() ([]goja.Value, error) {
	var roots []goja.Value
	var global = z.realm.vm.GlobalObject()
	globals, err := pristineGlobals()
	if err != nil {
		return nil, err
	}
	for _, key := range globals {
		described, err := z.describe(global, key)
		switch {
		case err != nil:
			return nil, err
		case described.absent():
		case described.isData():
			roots = append(roots, described.field("value"))
			err = lock(global, key, goja.PropertyDescriptor{Writable: goja.FLAG_FALSE, Configurable: goja.FLAG_FALSE})
		case described.isAccessor():
			err = lock(global, key, goja.PropertyDescriptor{Configurable: goja.FLAG_FALSE})
		default:
			err = shadowed(key)
		}
		if err != nil {
			return nil, err
		}
	}
	roots = append(roots, z.intrinsics.originalRoots...)
	for _, home := range z.intrinsics.homes {
		for _, key := range home.keys {
			value, err := z.overridableAt(home.object, key)
			if err != nil {
				return nil, err
			}
			roots = append(roots, value)
		}
	}
	return roots, nil
}

func lock(global *goja.Object, key goja.Value, locked goja.PropertyDescriptor) error {
	if symbol, isSymbol := key.(*goja.Symbol); isSymbol {
		return global.DefineDataPropertySymbol(symbol, nil, locked.Writable, locked.Configurable, goja.FLAG_NOT_SET)
	}
	return global.DefineDataProperty(key.String(), nil, locked.Writable, locked.Configurable, goja.FLAG_NOT_SET)
}

func shadowed(key any) error {
	return fmt.Errorf("a script shadowed a property descriptor field on Object.prototype, so %v cannot be frozen", key)
}

func (z _Freezing) overridableAt(home goja.Value, key string) (goja.Value, error) {
	var object, isObject = home.(*goja.Object)
	if !isObject {
		return goja.Null(), nil
	}
	described, err := z.describe(object, z.realm.vm.ToValue(key))
	if err != nil || !described.isData() || !described.field("configurable").ToBoolean() {
		return goja.Null(), err
	}
	var value = described.field("value")
	var getter = _Native{name: "get " + key, call: func(goja.FunctionCall) goja.Value {
		return value
	}}.on(z.realm.vm)
	var setter = _Native{name: "set " + key, length: 1, call: _Override{freezing: z, home: object, key: key}.set}.on(z.realm.vm)
	var enumerable = goja.FLAG_FALSE
	if described.field("enumerable").ToBoolean() {
		enumerable = goja.FLAG_TRUE
	}
	return value, object.DefineAccessorProperty(key, getter, setter, goja.FLAG_TRUE, enumerable)
}

type _Override struct {
	freezing _Freezing
	home     *goja.Object
	key      string
}

func (o _Override) set(call goja.FunctionCall) goja.Value {
	var vm = o.freezing.realm.vm
	target, isObject := call.This.(*goja.Object)
	if !isObject || target.SameAs(o.home) {
		panic(vm.NewTypeError(readOnlyRefusal))
	}
	own, err := o.freezing.describe(target, vm.ToValue(o.key))
	if err != nil {
		panic(err)
	}
	var fields = map[string]goja.Value{"value": call.Argument(0)}
	switch {
	case own.absent():
		fields = map[string]goja.Value{"value": call.Argument(0), "writable": vm.ToValue(true), "enumerable": vm.ToValue(true), "configurable": vm.ToValue(true)}
	case !own.holds("writable") || !own.field("writable").StrictEquals(vm.ToValue(true)):
		panic(vm.NewTypeError(readOnlyRefusal))
	}
	var replacement = vm.NewObject()
	if err := replacement.SetPrototype(nil); err != nil {
		panic(err)
	}
	for field, value := range fields {
		if err := replacement.Set(field, value); err != nil {
			panic(err)
		}
	}
	settled, err := o.freezing.intrinsics.define(goja.Undefined(), target, vm.ToValue(o.key), replacement)
	if err != nil {
		panic(err)
	}
	if !settled.ToBoolean() {
		panic(vm.NewTypeError(readOnlyRefusal))
	}
	return goja.Undefined()
}

func (z _Freezing) looseAccessors() ([]goja.Value, error) {
	var global = z.realm.vm.GlobalObject()
	keys, err := z.ownKeys(global)
	if err != nil {
		return nil, err
	}
	var loose []goja.Value
	for _, key := range keys {
		described, err := z.describe(global, key)
		switch {
		case err != nil:
			return nil, err
		case described.isData():
		case described.isAccessor():
			loose = append(loose, described.field("get"), described.field("set"))
		default:
			return nil, shadowed(key)
		}
	}
	return loose, nil
}

type _Step int

const (
	rootStep _Step = iota
	valueStep
	getterStep
	setterStep
	prototypeStep
)

type _Route struct {
	from int
	step _Step
	key  goja.Value
}

type _Reached struct {
	object *goja.Object
	route  _Route
}

type _Pending struct {
	value goja.Value
	route _Route
}

func (z _Freezing) walk(roots []goja.Value, seen map[*goja.Object]int) ([]_Reached, error) {
	var global = z.realm.vm.GlobalObject()
	var pending []_Pending
	for index := len(roots) - 1; index >= 0; index-- {
		pending = append(pending, _Pending{value: roots[index], route: _Route{from: index, step: rootStep}})
	}
	var reached []_Reached
	for len(pending) > 0 {
		var next = pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		object, isObject := next.value.(*goja.Object)
		if !isObject || object.SameAs(global) {
			continue
		}
		if _, known := seen[object]; known {
			continue
		}
		seen[object] = len(reached)
		reached = append(reached, _Reached{object: object, route: next.route})
		found, err := z.inside(object, seen[object])
		if err != nil {
			return nil, err
		}
		pending = append(pending, found...)
	}
	return reached, nil
}

func (z _Freezing) inside(object *goja.Object, id int) ([]_Pending, error) {
	keys, err := z.ownKeys(object)
	if err != nil {
		return nil, err
	}
	var found []_Pending
	for _, key := range keys {
		described, err := z.describe(object, key)
		switch {
		case err != nil:
			return nil, err
		case described.isData():
			found = append(found, _Pending{value: described.field("value"), route: _Route{from: id, step: valueStep, key: key}})
		case described.isAccessor():
			found = append(found, _Pending{value: described.field("set"), route: _Route{from: id, step: setterStep, key: key}}, _Pending{value: described.field("get"), route: _Route{from: id, step: getterStep, key: key}})
		default:
			return nil, shadowed(key)
		}
	}
	var prototype goja.Value = goja.Null()
	if inherited := object.Prototype(); inherited != nil {
		prototype = inherited
	}
	return append(found, _Pending{value: prototype, route: _Route{from: id, step: prototypeStep}}), nil
}

func (z _Freezing) ownKeys(object *goja.Object) ([]goja.Value, error) {
	listed, err := z.intrinsics.ownKeys(goja.Undefined(), object)
	if err != nil {
		return nil, err
	}
	return listedKeys(z.realm.vm, listed), nil
}

func listedKeys(vm *goja.Runtime, listed goja.Value) []goja.Value {
	var array = listed.ToObject(vm)
	var keys = make([]goja.Value, array.Get("length").ToInteger())
	for index := range keys {
		keys[index] = array.Get(strconv.Itoa(index))
	}
	return keys
}

func (z _Freezing) describe(object *goja.Object, key goja.Value) (_Described, error) {
	described, err := z.intrinsics.describe(goja.Undefined(), object, key)
	if err != nil {
		return _Described{}, err
	}
	fields, isObject := described.(*goja.Object)
	if !isObject {
		return _Described{}, nil
	}
	return _Described{fields: fields, names: fields.GetOwnPropertyNames()}, nil
}

type _Described struct {
	fields *goja.Object
	names  []string
}

func (d _Described) absent() bool {
	return d.fields == nil
}

func (d _Described) holds(field string) bool {
	return slices.Contains(d.names, field)
}

func (d _Described) field(field string) goja.Value {
	return d.fields.Get(field)
}

func (d _Described) isData() bool {
	return d.holds("value")
}

func (d _Described) isAccessor() bool {
	return d.holds("get") && d.holds("set")
}

func (z _Freezing) freezeAll(objects []*goja.Object) error {
	for _, object := range objects {
		if _, err := z.realm.freeze(goja.Undefined(), object); err != nil {
			return err
		}
	}
	return nil
}

func objectsOf(reached []_Reached) []*goja.Object {
	var objects = make([]*goja.Object, len(reached))
	for index, each := range reached {
		objects[index] = each.object
	}
	return objects
}

func walkTheBuiltIns(z _Freezing, roots []goja.Value) ([]*goja.Object, error) {
	reached, err := z.walk(roots, map[*goja.Object]int{})
	return objectsOf(reached), err
}

func replayTheChart(z _Freezing, roots []goja.Value) ([]*goja.Object, error) {
	chart, err := pristineChart()
	if err != nil {
		return nil, fmt.Errorf("charting the built-ins: %w", err)
	}
	var reached = make([]*goja.Object, 0, len(chart))
	for _, route := range chart {
		object, err := z.follow(route, roots, reached)
		if err != nil {
			return nil, err
		}
		reached = append(reached, object)
	}
	return reached, nil
}

func (z _Freezing) follow(route _Route, roots []goja.Value, reached []*goja.Object) (*goja.Object, error) {
	var found goja.Value
	switch route.step {
	case rootStep:
		found = roots[route.from]
	case valueStep:
		found = valueAt(reached[route.from], route.key)
	case getterStep, setterStep:
		described, err := z.describe(reached[route.from], route.key)
		if err != nil {
			return nil, err
		}
		found = described.field(map[_Step]string{getterStep: "get", setterStep: "set"}[route.step])
	case prototypeStep:
		found = reached[route.from].Prototype()
	}
	object, isObject := found.(*goja.Object)
	if !isObject {
		return nil, fmt.Errorf("the built-ins no longer match their chart")
	}
	return object, nil
}

func valueAt(object *goja.Object, key goja.Value) goja.Value {
	if symbol, isSymbol := key.(*goja.Symbol); isSymbol {
		return object.GetSymbol(symbol)
	}
	return object.Get(key.String())
}

var pristineGlobals = sync.OnceValues(func() ([]goja.Value, error) {
	var vm = goja.New()
	listed, err := method(vm.Get("Reflect").ToObject(vm), "ownKeys")(goja.Undefined(), vm.GlobalObject())
	if err != nil {
		return nil, err
	}
	return listedKeys(vm, listed), nil
})

var pristineChart = sync.OnceValues(func() ([]_Route, error) {
	var vm = goja.New()
	realm, err := captureRealm(_Pace{ctx: context.Background(), vm: vm}, _Runtime{builtins: frozenBuiltins})
	if err != nil {
		return nil, err
	}
	var frozen = realm.protection.(_Frozen)
	var freezing = _Freezing{realm: realm, intrinsics: frozen.intrinsics}
	roots, err := freezing.tame()
	if err != nil {
		return nil, err
	}
	reached, err := freezing.walk(roots, map[*goja.Object]int{})
	if err != nil {
		return nil, err
	}
	var routes = make([]_Route, len(reached))
	for index, each := range reached {
		routes[index] = each.route
	}
	return routes, nil
})

func chartTheIntrinsics() {
	_, _ = pristineChart()
}

type _Builtins int

const (
	frozenBuiltins _Builtins = iota
	mutableBuiltins
)

var builtinsNames = map[_Builtins]string{frozenBuiltins: "frozen", mutableBuiltins: "mutable"}

func (b _Builtins) String() string {
	return builtinsNames[b]
}

func builtinsNamed(name string) (_Builtins, bool) {
	for builtins, known := range builtinsNames {
		if known == name {
			return builtins, true
		}
	}
	return frozenBuiltins, false
}
