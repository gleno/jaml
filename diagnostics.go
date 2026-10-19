package jaml

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
)

const (
	codeExcerptLength   = 60
	keyExcerptLength    = 60
	thrownExcerptLength = 200
)

const unreadableThrow = "threw a value whose message cannot be read"

var enginePackage = reflect.TypeFor[goja.Runtime]().PkgPath()

func excerpt(text string, limit int) string {
	return clip(strings.Join(strings.Fields(strings.ToValidUTF8(text, "\uFFFD")), " "), limit)
}

func clip(text string, limit int) string {
	cut := 0
	for range limit {
		if cut >= len(text) {
			return text
		}
		_, size := utf8.DecodeRuneInString(text[cut:])
		cut += size
	}
	if cut >= len(text) {
		return text
	}
	return text[:cut] + "…"
}

func (r realm) explain(err error) error {
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return r.describeException(exception)
	}
	return uncatchable(err)
}

func uncatchable(err error) error {
	var overflowed *goja.StackOverflowError
	if errors.As(err, &overflowed) {
		return fmt.Errorf("exceeded the maximum call depth of %d", maxCallStackSize)
	}
	if holdsEngineValue(err) {
		return fmt.Errorf("the engine stopped with a %T", err)
	}
	return err
}

func (r realm) describeException(exception *goja.Exception) *Thrown {
	return newThrown(r.describe(exception.Value()), r.snapshot(exception.Value()))
}

func (r realm) describe(value goja.Value) string {
	var text string
	if err := r.contain(func() { text = r.describeThrown(value) }); err != nil {
		return unreadableThrow
	}
	return excerpt(text, thrownExcerptLength)
}

func holdsEngineValue(err error) bool {
	pending := []error{err}
	for len(pending) > 0 {
		next := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if isEngineValue(next) {
			return true
		}
		switch wrapping := next.(type) {
		case interface{ Unwrap() error }:
			if inner := wrapping.Unwrap(); inner != nil {
				pending = append(pending, inner)
			}
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapping.Unwrap()...)
		}
	}
	return false
}

func isEngineValue(value any) bool {
	kind := reflect.TypeOf(value)
	for kind != nil && kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	return kind != nil && strings.HasPrefix(kind.PkgPath(), enginePackage)
}

func (r realm) describeThrown(value goja.Value) string {
	object, isObject := value.(*goja.Object)
	switch {
	case isEmptyString(value):
		return "threw an empty string"
	case !isObject:
		return "threw " + primitiveText(value)
	case object.ExportType() == proxyType:
		return "threw a proxy"
	}
	if text := errorText(object); text != "" {
		return text
	}
	return "threw " + r.describeObject(object)
}

func (r realm) describeObject(object *goja.Object) string {
	if _, isFunction := goja.AssertFunction(object); isFunction {
		return "a function"
	}
	switch {
	case object.ClassName() == "Array":
		return "an array"
	case r.isPlain(object):
		return "an object"
	}
	return describeInstance(object)
}

func errorText(object *goja.Object) string {
	message, hasMessage := primitiveField(object, "message")
	if !hasMessage {
		return ""
	}
	name, _ := primitiveField(object, "name")
	return strings.Join(slices.DeleteFunc([]string{name, message}, func(part string) bool { return part == "" }), ": ")
}

func primitiveField(object *goja.Object, key string) (string, bool) {
	field := object.Get(key)
	if field == nil || goja.IsUndefined(field) || goja.IsNull(field) {
		return "", false
	}
	if _, isObject := field.(*goja.Object); isObject {
		return "", false
	}
	return primitiveText(field), true
}

func isEmptyString(value goja.Value) bool {
	written, isString := value.(goja.String)
	return isString && written.Length() == 0
}

func primitiveText(primitive goja.Value) string {
	if symbol, isSymbol := primitive.(*goja.Symbol); isSymbol {
		return "Symbol(" + symbol.String() + ")"
	}
	if integer, isBigInt := primitive.Export().(*big.Int); isBigInt {
		return integer.String() + "n"
	}
	return primitive.String()
}
