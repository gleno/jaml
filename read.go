package jaml

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
)

const (
	maxResultValues = 1 << 20
	maxDataDepth    = 4096
	notData         = ", which is not data"
)

var (
	proxyType    = reflect.TypeFor[goja.Proxy]()
	nestsTooDeep = fmt.Sprintf("nests deeper than %d", maxDataDepth)
)

type _Undefined struct{}

type _Realm struct {
	vm              *goja.Runtime
	objectPrototype *goja.Object
	arrayPrototype  *goja.Object
	ownKeys         goja.Callable
	freeze          goja.Callable
	parse           goja.Callable
}

func captureRealm(vm *goja.Runtime) _Realm {
	var object = vm.Get("Object").ToObject(vm)
	ownKeys, _ := goja.AssertFunction(object.Get("keys"))
	freeze, _ := goja.AssertFunction(object.Get("freeze"))
	var arrayPrototype = vm.Get("Array").ToObject(vm).Get("prototype").ToObject(vm)
	return _Realm{vm: vm, objectPrototype: object.Get("prototype").ToObject(vm), arrayPrototype: arrayPrototype, ownKeys: ownKeys, freeze: freeze, parse: guardJSONParse(vm)}
}

type _Native struct {
	name   string
	length int
	call   func(goja.FunctionCall) goja.Value
}

func (n _Native) on(vm *goja.Runtime) *goja.Object {
	var function = vm.ToValue(n.call).ToObject(vm)
	for key, value := range map[string]any{"name": n.name, "length": n.length} {
		if err := function.DefineDataProperty(key, vm.ToValue(value), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE); err != nil {
			panic(err)
		}
	}
	return function
}

func (r _Realm) native(run func()) error {
	native, _ := goja.AssertFunction(_Native{call: func(goja.FunctionCall) goja.Value {
		run()
		return goja.Undefined()
	}}.on(r.vm))
	_, err := native(goja.Undefined())
	return err
}

func (r _Realm) contain(run func()) error {
	var err = r.native(run)
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		panic(interrupted)
	}
	return err
}

func (r _Realm) isData(object *goja.Object) bool {
	var prototype = object.Prototype()
	switch object.ClassName() {
	case "Object":
		return prototype == nil || prototype.SameAs(r.objectPrototype)
	case "Array":
		return prototype == nil || prototype.SameAs(r.arrayPrototype)
	}
	return false
}

func (r _Realm) describeInstance(object *goja.Object) string {
	var plain, custom = r.objectPrototype, "an object with a custom prototype"
	switch object.ClassName() {
	case "Object":
	case "Array":
		plain, custom = r.arrayPrototype, "an array with a custom prototype"
	default:
		return "an instance of " + object.ClassName()
	}
	constructor, isObject := object.Get("constructor").(*goja.Object)
	if !isObject || constructor.SameAs(plain.Get("constructor")) {
		return custom
	}
	name, isString := constructor.Get("name").(goja.String)
	if !isString || name.Length() == 0 {
		return custom
	}
	return "an instance of " + excerpt(name.String(), codeExcerptLength)
}

type _NotData struct {
	path    *_DataPath
	claim   string
	leftOut bool
}

func (n _NotData) refusal() error {
	return fmt.Errorf("%s %s", describeResultAt(n.path), n.claim)
}

type _ReadPolicy interface {
	stranger(part _NotData) (any, error)
	instance(refusal func() error) (asFields bool, err error)
}

type _ResultPolicy struct{}

func (_ResultPolicy) stranger(part _NotData) (any, error) {
	return nil, part.refusal()
}

func (_ResultPolicy) instance(refusal func() error) (bool, error) {
	return false, refusal()
}

type _FailedRead struct {
	path  *_DataPath
	cause error
}

func (f *_FailedRead) Error() string {
	return "reading " + describeResultAt(f.path) + ": " + f.cause.Error()
}

type _DataReader struct {
	realm     _Realm
	policy    _ReadPolicy
	ancestors map[*goja.Object]bool
	remaining int
}

func readResult(realm _Realm, returned goja.Value) (any, error) {
	result, err := readData(realm, returned, _ResultPolicy{})
	var failed *_FailedRead
	if errors.As(err, &failed) {
		return nil, fmt.Errorf("reading %s: %w", describeResultAt(failed.path), realm.explain(failed.cause))
	}
	return result, err
}

func readData(realm _Realm, value goja.Value, policy _ReadPolicy) (any, error) {
	var reader = &_DataReader{realm: realm, policy: policy, ancestors: map[*goja.Object]bool{}, remaining: maxResultValues}
	if err := reader.pay(1); err != nil {
		return nil, err
	}
	return reader.read(value, nil)
}

func (r *_DataReader) pay(values int64) error {
	if values > int64(r.remaining) {
		return fmt.Errorf("result holds more than %d values", maxResultValues)
	}
	r.remaining -= int(values)
	return nil
}

func (r *_DataReader) stranger(path *_DataPath, claim string, leftOut bool) (any, error) {
	return r.policy.stranger(_NotData{path: path, claim: claim, leftOut: leftOut})
}

func (r *_DataReader) read(value goja.Value, path *_DataPath) (any, error) {
	switch {
	case value == nil || goja.IsUndefined(value):
		return _Undefined{}, nil
	case goja.IsNull(value):
		return nil, nil
	}
	switch typed := value.(type) {
	case *goja.Object:
		return r.readObject(typed, path)
	case *goja.Symbol:
		return r.stranger(path, "is a symbol"+notData, true)
	case goja.String:
		if text, wellFormed := wellFormedText(typed); wellFormed {
			return text, nil
		}
		return r.stranger(path, "is a string with an unpaired surrogate"+notData, false)
	}
	return r.readPrimitive(value, path)
}

func (r *_DataReader) readPrimitive(primitive goja.Value, path *_DataPath) (any, error) {
	switch exported := primitive.Export().(type) {
	case bool:
		return exported, nil
	case int64:
		return exported, nil
	case float64:
		return r.readFloat(exported, path)
	case *big.Int:
		return r.stranger(path, "is a BigInt"+notData, false)
	default:
		return r.stranger(path, "is a value of no JavaScript type jaml knows"+notData, false)
	}
}

func (r *_DataReader) readFloat(number float64, path *_DataPath) (any, error) {
	switch {
	case math.IsNaN(number):
		return r.stranger(path, "is NaN"+notData, false)
	case math.IsInf(number, 1):
		return r.stranger(path, "is Infinity"+notData, false)
	case math.IsInf(number, -1):
		return r.stranger(path, "is -Infinity"+notData, false)
	case number == 0:
		return float64(0), nil
	default:
		return number, nil
	}
}

func (r *_DataReader) readObject(object *goja.Object, path *_DataPath) (any, error) {
	if object.ExportType() == proxyType {
		return r.stranger(path, "is a proxy"+notData, false)
	}
	if _, isFunction := goja.AssertFunction(object); isFunction {
		return r.stranger(path, "is a function"+notData, true)
	}
	if r.ancestors[object] {
		return r.stranger(path, "is a cyclic reference"+notData, false)
	}
	if path.depth() >= maxDataDepth {
		return r.stranger(path, nestsTooDeep, false)
	}
	r.ancestors[object] = true
	defer delete(r.ancestors, object)
	if !r.realm.isData(object) {
		asFields, err := r.policy.instance(func() error { return r.refuseInstance(object, path) })
		if err != nil || !asFields {
			return nil, err
		}
	}
	if object.ClassName() == "Array" {
		return r.readArray(object, path)
	}
	return r.readFields(object, path)
}

func (r *_DataReader) refuseInstance(object *goja.Object, path *_DataPath) error {
	var described string
	if err := r.attempt(path, func() { described = r.realm.describeInstance(object) }); err != nil {
		return err
	}
	return fmt.Errorf("%s is %s%s", describeResultAt(path), described, notData)
}

func (r *_DataReader) attempt(path *_DataPath, run func()) error {
	if exception := r.realm.vm.Try(run); exception != nil {
		return &_FailedRead{path: path, cause: exception}
	}
	return nil
}

func (r *_DataReader) property(object *goja.Object, key string, path *_DataPath) (goja.Value, error) {
	var value goja.Value
	var err = r.attempt(path, func() { value = object.Get(key) })
	return value, err
}

func (r *_DataReader) readArray(array *goja.Object, path *_DataPath) (any, error) {
	var length = array.Get("length").ToInteger()
	if err := r.pay(length); err != nil {
		return nil, err
	}
	var list = make([]any, length)
	for i := range list {
		var itemPath = path.atIndex(i)
		value, err := r.property(array, strconv.Itoa(i), itemPath)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, itemPath)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(_Undefined); !isUndefined {
			list[i] = item
		}
	}
	return list, nil
}

func (r *_DataReader) readFields(object *goja.Object, path *_DataPath) (any, error) {
	keys, err := r.keysOf(object, path)
	if err != nil {
		return nil, err
	}
	var fields = make(map[string]any, len(keys))
	for _, key := range keys {
		var fieldPath = path.atKey(key)
		value, err := r.property(object, key, fieldPath)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, fieldPath)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(_Undefined); !isUndefined {
			fields[key] = item
		}
	}
	return fields, nil
}

func (r *_DataReader) keysOf(object *goja.Object, path *_DataPath) ([]string, error) {
	listed, err := r.realm.ownKeys(goja.Undefined(), object)
	if err != nil {
		return nil, &_FailedRead{path: path, cause: err}
	}
	written, isObject := listed.(*goja.Object)
	if !isObject {
		return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(path))
	}
	var count = written.Get("length").ToInteger()
	if err := r.pay(count); err != nil {
		return nil, err
	}
	var keys = make([]string, 0, count)
	for i := range count {
		listedKey, isString := written.Get(strconv.FormatInt(i, 10)).(goja.String)
		if !isString {
			return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(path))
		}
		key, wellFormed := wellFormedText(listedKey)
		if wellFormed {
			keys = append(keys, key)
			continue
		}
		if _, err := r.stranger(path, "has a key with an unpaired surrogate"+notData, true); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func wellFormedText(written goja.String) (string, bool) {
	var text = written.String()
	if !strings.ContainsRune(text, utf8.RuneError) {
		return text, true
	}
	var units = func(yield func(uint16) bool) {
		for i := range written.Length() {
			if !yield(written.CharAt(i)) {
				return
			}
		}
	}
	if !wellFormedUnits(units) {
		return "", false
	}
	return text, true
}

func tooDeep(what string) error {
	return fmt.Errorf("%s %s", what, nestsTooDeep)
}

func describeResultAt(path *_DataPath) string {
	return path.spelledFrom("result")
}
