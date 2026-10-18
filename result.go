package jaml

import (
	"fmt"
	"math"
	"strconv"

	"github.com/dop251/goja"
)

const maxResultNodes = 1 << 20

type resultReader struct {
	objectPrototype *goja.Object
	ancestors       map[*goja.Object]bool
	nodes           int
}

func (r *resultReader) read(returned goja.Value, at location) (value, error) {
	r.nodes++
	if r.nodes > maxResultNodes {
		return nil, fmt.Errorf("%s holds more than %d values", describeResultAt(at), maxResultNodes)
	}
	if returned == nil || goja.IsUndefined(returned) {
		return undefinedValue{}, nil
	}
	if goja.IsNull(returned) {
		return nullValue{}, nil
	}
	if object, isObject := returned.(*goja.Object); isObject {
		return r.readObject(object, at)
	}
	return readPrimitive(returned, at)
}

func readPrimitive(primitive goja.Value, at location) (value, error) {
	if _, isSymbol := primitive.(*goja.Symbol); isSymbol {
		return nil, notJamlable(at, "a symbol")
	}
	switch exported := primitive.Export().(type) {
	case bool:
		return boolValue{truth: exported}, nil
	case string:
		return stringValue(exported), nil
	case int64:
		return numberValue{text: strconv.FormatInt(exported, 10)}, nil
	case float64:
		return readFloat(exported, primitive, at)
	default:
		return nil, notJamlable(at, fmt.Sprintf("a %T", exported))
	}
}

func readFloat(number float64, primitive goja.Value, at location) (value, error) {
	switch {
	case math.IsNaN(number):
		return nil, notJamlable(at, "NaN")
	case math.IsInf(number, 0):
		return nil, notJamlable(at, primitive.String())
	case number == 0:
		return numberValue{text: "0"}, nil
	default:
		return numberValue{text: primitive.String()}, nil
	}
}

func (r *resultReader) readObject(object *goja.Object, at location) (value, error) {
	if _, isFunction := goja.AssertFunction(object); isFunction {
		return nil, notJamlable(at, "a function")
	}
	if r.ancestors[object] {
		return nil, notJamlable(at, "a cyclic reference")
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
	return prototype == nil || prototype.SameAs(r.objectPrototype)
}

func (r *resultReader) readArray(array *goja.Object, at location) (value, error) {
	length := array.Get("length").ToInteger()
	if length > maxResultNodes {
		return nil, fmt.Errorf("%s holds more than %d values", describeResultAt(at), maxResultNodes)
	}
	list := make(listValue, 0, length)
	for i := range int(length) {
		item, err := r.read(array.Get(strconv.Itoa(i)), at.index(i, position{}))
		if err != nil {
			return nil, err
		}
		list = append(list, element{value: item})
	}
	return list, nil
}

func (r *resultReader) readPlainObject(object *goja.Object, at location) (value, error) {
	keys := object.Keys()
	fields := make(objectValue, 0, len(keys))
	for _, key := range keys {
		item, err := r.read(object.Get(key), at.key(key, position{}))
		if err != nil {
			return nil, err
		}
		fields = append(fields, field{key: key, value: item})
	}
	return fields, nil
}

func notJamlable(at location, what string) error {
	return fmt.Errorf("%s is %s, which is not YAML data", describeResultAt(at), what)
}

func describeResultAt(at location) string {
	return "result" + at.path()[1:]
}
