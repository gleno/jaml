package jaml

import (
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
	objectPrototype *goja.Object
	ownKeys         goja.Callable
}

func captureRealm(vm *goja.Runtime) realm {
	object := vm.Get("Object").ToObject(vm)
	ownKeys, _ := goja.AssertFunction(object.Get("keys"))
	return realm{objectPrototype: object.Get("prototype").ToObject(vm), ownKeys: ownKeys}
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
	case object.ClassName() == "Object" && r.isPlain(object):
		return r.readPlainObject(object, at)
	default:
		return nil, notJamlable(at, describeInstance(object))
	}
}

func describeInstance(object *goja.Object) string {
	if object.ClassName() != "Object" {
		return "an instance of " + object.ClassName()
	}
	constructor, isObject := object.Get("constructor").(*goja.Object)
	if !isObject {
		return "an object with a custom prototype"
	}
	name := constructor.Get("name")
	if name == nil || name.String() == "" {
		return "an object with a custom prototype"
	}
	return "an instance of " + name.String()
}

func (r *resultReader) isPlain(object *goja.Object) bool {
	prototype := object.Prototype()
	return prototype == nil || prototype.SameAs(r.realm.objectPrototype)
}

func (r *resultReader) readArray(array *goja.Object, at *dataPath) (any, error) {
	length := array.Get("length").ToInteger()
	if err := r.pay(length); err != nil {
		return nil, err
	}
	list := make([]any, length)
	for i := range list {
		item, err := r.read(array.Get(strconv.Itoa(i)), at.atIndex(i))
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
		item, err := r.read(object.Get(key), at.atKey(key))
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
		return nil, err
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
