package jaml

import (
	"math"
	"math/big"
	"strconv"

	"github.com/dop251/goja"
)

type Thrown struct {
	Message string
	Value   any
}

func (t *Thrown) Error() string {
	return t.Message
}

type snapshotReader struct {
	realm     realm
	ancestors map[*goja.Object]bool
	remaining int
}

func (r realm) snapshot(thrown goja.Value) any {
	reader := &snapshotReader{realm: r, ancestors: map[*goja.Object]bool{}, remaining: maxResultValues}
	var taken any
	complete := false
	if err := r.guarded(func() { taken, complete = reader.take(thrown, 0) }); err != nil || !complete {
		return nil
	}
	return taken
}

func (r *snapshotReader) pay(values int64) bool {
	if values > int64(r.remaining) {
		return false
	}
	r.remaining -= int(values)
	return true
}

func (r *snapshotReader) take(value goja.Value, depth int) (any, bool) {
	if !r.pay(1) {
		return nil, false
	}
	switch typed := value.(type) {
	case nil:
		return nil, true
	case *goja.Object:
		return r.takeObject(typed, depth)
	case *goja.Symbol:
		return nil, true
	case goja.String:
		if text, wellFormed := wellFormedText(typed); wellFormed {
			return text, true
		}
		return nil, true
	}
	return snapshotPrimitive(value), true
}

func snapshotPrimitive(primitive goja.Value) any {
	switch exported := primitive.Export().(type) {
	case bool:
		return exported
	case int64:
		return float64(exported)
	case float64:
		if math.IsNaN(exported) || math.IsInf(exported, 0) {
			return nil
		}
		return exported + 0
	case *big.Int:
		return nil
	}
	return nil
}

func (r *snapshotReader) takeObject(object *goja.Object, depth int) (any, bool) {
	if _, isFunction := goja.AssertFunction(object); isFunction || object.ExportType() == proxyType || r.ancestors[object] || depth >= maxDataDepth {
		return nil, true
	}
	r.ancestors[object] = true
	defer delete(r.ancestors, object)
	if object.ClassName() == "Array" {
		return r.takeArray(object, depth)
	}
	return r.takeFields(object, depth)
}

func (r *snapshotReader) takeArray(array *goja.Object, depth int) (any, bool) {
	length := array.Get("length").ToInteger()
	if !r.pay(length) {
		return nil, false
	}
	list := make([]any, length)
	for i := range list {
		item, complete := r.take(array.Get(strconv.Itoa(i)), depth+1)
		if !complete {
			return nil, false
		}
		list[i] = item
	}
	return list, true
}

func (r *snapshotReader) takeFields(object *goja.Object, depth int) (any, bool) {
	listed, err := r.realm.ownKeys(goja.Undefined(), object)
	if err != nil {
		panic(err)
	}
	keys := listed.ToObject(r.realm.vm)
	count := keys.Get("length").ToInteger()
	if !r.pay(count) {
		return nil, false
	}
	fields := make(map[string]any, count)
	for i := range count {
		listedKey, isString := keys.Get(strconv.FormatInt(i, 10)).(goja.String)
		if !isString {
			continue
		}
		key, wellFormed := wellFormedText(listedKey)
		if !wellFormed {
			continue
		}
		field := object.Get(key)
		if omittedFromJSON(field) {
			continue
		}
		item, complete := r.take(field, depth+1)
		if !complete {
			return nil, false
		}
		fields[key] = item
	}
	return fields, true
}

func omittedFromJSON(field goja.Value) bool {
	if field == nil || goja.IsUndefined(field) {
		return true
	}
	if _, isSymbol := field.(*goja.Symbol); isSymbol {
		return true
	}
	object, isObject := field.(*goja.Object)
	if !isObject {
		return false
	}
	_, isFunction := goja.AssertFunction(object)
	return isFunction
}
