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
	notYAML         = ", which is not YAML data"
)

var (
	proxyType    = reflect.TypeFor[goja.Proxy]()
	nestsTooDeep = fmt.Sprintf("nests deeper than %d", maxDataDepth)
)

type _Undefined struct{}

type _Realm struct {
	vm              *goja.Runtime
	objectPrototype *goja.Object
	ownKeys         goja.Callable
	freeze          goja.Callable
	parse           goja.Callable
}

func captureRealm(vm *goja.Runtime) _Realm {
	var object = vm.Get("Object").ToObject(vm)
	ownKeys, _ := goja.AssertFunction(object.Get("keys"))
	freeze, _ := goja.AssertFunction(object.Get("freeze"))
	return _Realm{vm: vm, objectPrototype: object.Get("prototype").ToObject(vm), ownKeys: ownKeys, freeze: freeze, parse: guardJSONParse(vm)}
}

func (r _Realm) native(run func()) error {
	native, _ := goja.AssertFunction(r.vm.ToValue(func(goja.FunctionCall) goja.Value {
		run()
		return goja.Undefined()
	}))
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

func (r _Realm) isPlain(object *goja.Object) bool {
	var prototype = object.Prototype()
	return prototype == nil || prototype.SameAs(r.objectPrototype)
}

type _NotData struct {
	at      *_DataPath
	claim   string
	leftOut bool
}

func (n _NotData) refusal() error {
	return fmt.Errorf("%s %s", describeResultAt(n.at), n.claim)
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
	at    *_DataPath
	cause error
}

func (f *_FailedRead) Error() string {
	return "reading " + describeResultAt(f.at) + ": " + f.cause.Error()
}

type _DataReader struct {
	realm     _Realm
	policy    _ReadPolicy
	ancestors map[*goja.Object]bool
	remaining int
}

func readResult(from _Realm, returned goja.Value) (any, error) {
	result, err := readData(from, returned, _ResultPolicy{})
	var failed *_FailedRead
	if errors.As(err, &failed) {
		return nil, fmt.Errorf("reading %s: %w", describeResultAt(failed.at), from.explain(failed.cause))
	}
	return result, err
}

func readData(from _Realm, value goja.Value, policy _ReadPolicy) (any, error) {
	var reader = &_DataReader{realm: from, policy: policy, ancestors: map[*goja.Object]bool{}, remaining: maxResultValues}
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

func (r *_DataReader) stranger(at *_DataPath, claim string, leftOut bool) (any, error) {
	return r.policy.stranger(_NotData{at: at, claim: claim, leftOut: leftOut})
}

func (r *_DataReader) read(value goja.Value, at *_DataPath) (any, error) {
	switch {
	case value == nil || goja.IsUndefined(value):
		return _Undefined{}, nil
	case goja.IsNull(value):
		return nil, nil
	}
	switch typed := value.(type) {
	case *goja.Object:
		return r.readObject(typed, at)
	case *goja.Symbol:
		return r.stranger(at, "is a symbol"+notYAML, true)
	case goja.String:
		if text, wellFormed := wellFormedText(typed); wellFormed {
			return text, nil
		}
		return r.stranger(at, "is a string with an unpaired surrogate"+notYAML, false)
	}
	return r.readPrimitive(value, at)
}

func (r *_DataReader) readPrimitive(primitive goja.Value, at *_DataPath) (any, error) {
	switch exported := primitive.Export().(type) {
	case bool:
		return exported, nil
	case int64:
		return exported, nil
	case float64:
		return r.readFloat(exported, at)
	case *big.Int:
		return r.stranger(at, "is a BigInt"+notYAML, false)
	default:
		return r.stranger(at, "is a value of no JavaScript type jaml knows"+notYAML, false)
	}
}

func (r *_DataReader) readFloat(number float64, at *_DataPath) (any, error) {
	switch {
	case math.IsNaN(number):
		return r.stranger(at, "is NaN"+notYAML, false)
	case math.IsInf(number, 1):
		return r.stranger(at, "is Infinity"+notYAML, false)
	case math.IsInf(number, -1):
		return r.stranger(at, "is -Infinity"+notYAML, false)
	case number == 0:
		return float64(0), nil
	default:
		return number, nil
	}
}

func (r *_DataReader) readObject(object *goja.Object, at *_DataPath) (any, error) {
	if object.ExportType() == proxyType {
		return r.stranger(at, "is a proxy"+notYAML, false)
	}
	if _, isFunction := goja.AssertFunction(object); isFunction {
		return r.stranger(at, "is a function"+notYAML, true)
	}
	if r.ancestors[object] {
		return r.stranger(at, "is a cyclic reference"+notYAML, false)
	}
	if at.depth() >= maxDataDepth {
		return r.stranger(at, nestsTooDeep, false)
	}
	r.ancestors[object] = true
	defer delete(r.ancestors, object)
	switch {
	case object.ClassName() == "Array":
		return r.readArray(object, at)
	case object.ClassName() == "Object" && r.realm.isPlain(object):
		return r.readFields(object, at)
	}
	asFields, err := r.policy.instance(func() error { return r.refuseInstance(object, at) })
	if err != nil || !asFields {
		return nil, err
	}
	return r.readFields(object, at)
}

func (r *_DataReader) refuseInstance(object *goja.Object, at *_DataPath) error {
	var described string
	if err := r.attempt(at, func() { described = describeInstance(object) }); err != nil {
		return err
	}
	return fmt.Errorf("%s is %s%s", describeResultAt(at), described, notYAML)
}

func (r *_DataReader) attempt(at *_DataPath, run func()) error {
	if exception := r.realm.vm.Try(run); exception != nil {
		return &_FailedRead{at: at, cause: exception}
	}
	return nil
}

func (r *_DataReader) property(object *goja.Object, key string, at *_DataPath) (goja.Value, error) {
	var value goja.Value
	var err = r.attempt(at, func() { value = object.Get(key) })
	return value, err
}

func describeInstance(object *goja.Object) string {
	if object.ClassName() != "Object" {
		return "an instance of " + object.ClassName()
	}
	constructor, isObject := object.Get("constructor").(*goja.Object)
	if !isObject {
		return "an object with a custom prototype"
	}
	name, isString := constructor.Get("name").(goja.String)
	if !isString || name.Length() == 0 {
		return "an object with a custom prototype"
	}
	return "an instance of " + excerpt(name.String(), codeExcerptLength)
}

func (r *_DataReader) readArray(array *goja.Object, at *_DataPath) (any, error) {
	var length = array.Get("length").ToInteger()
	if err := r.pay(length); err != nil {
		return nil, err
	}
	var list = make([]any, length)
	for i := range list {
		var itemAt = at.atIndex(i)
		value, err := r.property(array, strconv.Itoa(i), itemAt)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, itemAt)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(_Undefined); !isUndefined {
			list[i] = item
		}
	}
	return list, nil
}

func (r *_DataReader) readFields(object *goja.Object, at *_DataPath) (any, error) {
	keys, err := r.keysOf(object, at)
	if err != nil {
		return nil, err
	}
	var fields = make(map[string]any, len(keys))
	for _, key := range keys {
		var fieldAt = at.atKey(key)
		value, err := r.property(object, key, fieldAt)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, fieldAt)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(_Undefined); !isUndefined {
			fields[key] = item
		}
	}
	return fields, nil
}

func (r *_DataReader) keysOf(object *goja.Object, at *_DataPath) ([]string, error) {
	listed, err := r.realm.ownKeys(goja.Undefined(), object)
	if err != nil {
		return nil, &_FailedRead{at: at, cause: err}
	}
	written, isObject := listed.(*goja.Object)
	if !isObject {
		return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(at))
	}
	var count = written.Get("length").ToInteger()
	if err := r.pay(count); err != nil {
		return nil, err
	}
	var keys = make([]string, 0, count)
	for i := range count {
		listedKey, isString := written.Get(strconv.FormatInt(i, 10)).(goja.String)
		if !isString {
			return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(at))
		}
		key, wellFormed := wellFormedText(listedKey)
		if wellFormed {
			keys = append(keys, key)
			continue
		}
		if _, err := r.stranger(at, "has a key with an unpaired surrogate"+notYAML, true); err != nil {
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

func describeResultAt(at *_DataPath) string {
	return at.spelledFrom("result")
}
