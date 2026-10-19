package jaml

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dop251/goja"
)

const (
	maxResultValues = 1 << 20
	maxDataDepth    = 4096
)

var proxyType = reflect.TypeFor[goja.Proxy]()

type undefined struct{}

type realm struct {
	vm              *goja.Runtime
	objectPrototype *goja.Object
	ownKeys         goja.Callable
	parse           goja.Callable
}

func captureRealm(vm *goja.Runtime) realm {
	object := vm.Get("Object").ToObject(vm)
	ownKeys, _ := goja.AssertFunction(object.Get("keys"))
	return realm{vm: vm, objectPrototype: object.Get("prototype").ToObject(vm), ownKeys: ownKeys, parse: guardJSONParse(vm)}
}

func (r realm) native(run func()) error {
	native, _ := goja.AssertFunction(r.vm.ToValue(func(goja.FunctionCall) goja.Value {
		run()
		return goja.Undefined()
	}))
	_, err := native(goja.Undefined())
	return err
}

func (r realm) contain(run func()) error {
	err := r.native(run)
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		panic(interrupted)
	}
	return err
}

func (r realm) isPlain(object *goja.Object) bool {
	prototype := object.Prototype()
	return prototype == nil || prototype.SameAs(r.objectPrototype)
}

type resultReader struct {
	realm     realm
	ancestors map[*goja.Object]bool
	remaining int
}

func readResult(from realm, returned goja.Value) (any, error) {
	reader := &resultReader{realm: from, ancestors: map[*goja.Object]bool{}, remaining: maxResultValues}
	if err := reader.pay(1); err != nil {
		return nil, err
	}
	return reader.read(returned, nil)
}

func (r *resultReader) pay(values int64) error {
	if values > int64(r.remaining) {
		return fmt.Errorf("result holds more than %d values", maxResultValues)
	}
	r.remaining -= int(values)
	return nil
}

func (r *resultReader) read(returned goja.Value, at *dataPath) (any, error) {
	switch {
	case returned == nil || goja.IsUndefined(returned):
		return undefined{}, nil
	case goja.IsNull(returned):
		return nil, nil
	}
	switch typed := returned.(type) {
	case *goja.Object:
		return r.readObject(typed, at)
	case *goja.Symbol:
		return nil, notJamlable(at, "a symbol")
	case goja.String:
		return readString(typed, at)
	}
	return readPrimitive(returned, at)
}

func readString(written goja.String, at *dataPath) (any, error) {
	text, wellFormed := wellFormedText(written)
	if !wellFormed {
		return nil, notJamlable(at, "a string with an unpaired surrogate")
	}
	return text, nil
}

func wellFormedText(written goja.String) (string, bool) {
	text := written.String()
	if !strings.ContainsRune(text, utf8.RuneError) {
		return text, true
	}
	for i := 0; i < written.Length(); i++ {
		unit := rune(written.CharAt(i))
		if !utf16.IsSurrogate(unit) {
			continue
		}
		if unit >= 0xDC00 || i+1 == written.Length() {
			return "", false
		}
		next := rune(written.CharAt(i + 1))
		if next < 0xDC00 || next > 0xDFFF {
			return "", false
		}
		i++
	}
	return text, true
}

func readPrimitive(primitive goja.Value, at *dataPath) (any, error) {
	switch exported := primitive.Export().(type) {
	case bool:
		return exported, nil
	case int64:
		return exported, nil
	case float64:
		return readFloat(exported, primitive, at)
	case *big.Int:
		return nil, notJamlable(at, "a BigInt")
	default:
		return nil, notJamlable(at, "a value of no JavaScript type jaml knows")
	}
}

func readFloat(number float64, primitive goja.Value, at *dataPath) (any, error) {
	switch {
	case math.IsNaN(number):
		return nil, notJamlable(at, "NaN")
	case math.IsInf(number, 0):
		return nil, notJamlable(at, primitive.String())
	case number == 0:
		return float64(0), nil
	default:
		return number, nil
	}
}

func (r *resultReader) readObject(object *goja.Object, at *dataPath) (any, error) {
	if object.ExportType() == proxyType {
		return nil, notJamlable(at, "a proxy")
	}
	if _, isFunction := goja.AssertFunction(object); isFunction {
		return nil, notJamlable(at, "a function")
	}
	if r.ancestors[object] {
		return nil, notJamlable(at, "a cyclic reference")
	}
	if at.depth() >= maxDataDepth {
		return nil, tooDeep(describeResultAt(at))
	}
	r.ancestors[object] = true
	defer delete(r.ancestors, object)
	switch {
	case object.ClassName() == "Array":
		return r.readArray(object, at)
	case object.ClassName() == "Object" && r.realm.isPlain(object):
		return r.readPlainObject(object, at)
	}
	var described string
	if err := r.attempt(at, func() { described = describeInstance(object) }); err != nil {
		return nil, err
	}
	return nil, notJamlable(at, described)
}

func (r *resultReader) attempt(at *dataPath, run func()) error {
	if exception := r.realm.vm.Try(run); exception != nil {
		return r.failedReading(at, exception)
	}
	return nil
}

func (r *resultReader) failedReading(at *dataPath, err error) error {
	return fmt.Errorf("reading %s: %w", describeResultAt(at), r.realm.explain(err))
}

func (r *resultReader) property(object *goja.Object, key string, at *dataPath) (goja.Value, error) {
	var value goja.Value
	err := r.attempt(at, func() { value = object.Get(key) })
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

func (r *resultReader) readArray(array *goja.Object, at *dataPath) (any, error) {
	length := array.Get("length").ToInteger()
	if err := r.pay(length); err != nil {
		return nil, err
	}
	list := make([]any, length)
	for i := range list {
		itemAt := at.atIndex(i)
		value, err := r.property(array, strconv.Itoa(i), itemAt)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, itemAt)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(undefined); !isUndefined {
			list[i] = item
		}
	}
	return list, nil
}

func (r *resultReader) readPlainObject(object *goja.Object, at *dataPath) (any, error) {
	keys, err := r.keysOf(object, at)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]any, len(keys))
	for _, key := range keys {
		fieldAt := at.atKey(key)
		value, err := r.property(object, key, fieldAt)
		if err != nil {
			return nil, err
		}
		item, err := r.read(value, fieldAt)
		if err != nil {
			return nil, err
		}
		if _, isUndefined := item.(undefined); !isUndefined {
			fields[key] = item
		}
	}
	return fields, nil
}

func (r *resultReader) keysOf(object *goja.Object, at *dataPath) ([]string, error) {
	listed, err := r.realm.ownKeys(goja.Undefined(), object)
	if err != nil {
		return nil, r.failedReading(at, err)
	}
	written, isObject := listed.(*goja.Object)
	if !isObject {
		return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(at))
	}
	count := written.Get("length").ToInteger()
	if err := r.pay(count); err != nil {
		return nil, err
	}
	keys := make([]string, count)
	for i := range keys {
		listedKey, isString := written.Get(strconv.Itoa(i)).(goja.String)
		if !isString {
			return nil, fmt.Errorf("%s has keys that Object.keys cannot list", describeResultAt(at))
		}
		key, wellFormed := wellFormedText(listedKey)
		if !wellFormed {
			return nil, fmt.Errorf("%s has a key with an unpaired surrogate, which is not YAML data", describeResultAt(at))
		}
		keys[i] = key
	}
	return keys, nil
}

func notJamlable(at *dataPath, what string) error {
	return fmt.Errorf("%s is %s, which is not YAML data", describeResultAt(at), what)
}

func tooDeep(what string) error {
	return fmt.Errorf("%s nests deeper than %d", what, maxDataDepth)
}

func describeResultAt(at *dataPath) string {
	return at.spelledFrom("result")
}
