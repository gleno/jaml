package jaml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

type _DetachedLazyDecoder struct {
	Later *Expression
	Now   int
	Calls int
}

func (d *_DetachedLazyDecoder) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	d.Calls++
	data, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	var decoded struct {
		Later *Expression
		Now   int
	}
	err = json.Unmarshal(data, &decoded, decoder.Options())
	d.Later, d.Now = decoded.Later, decoded.Now
	return err
}

func TestJSONv2DetachedDecoderKeepsItsOwnSource(t *testing.T) {
	type _Target struct {
		Later  *Expression
		Opaque _DetachedLazyDecoder
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var reference _Target
		original := json.Unmarshal([]byte(`{"Later":null,"Opaque":{"Later":{}}}`), &reference)
		if original == nil {
			t.Fatal("native decoder accepted Expression without a custom representation")
		}
		got, err := Resolve[_Target](t.Context(), mustJaml(t, sb, "Later: '${1}'\nOpaque: {Later: {}}\n"))
		expectError(t, err, CoercionFailed, original.Error())
		expectEqual(t, got, reference)
		var want, have *json.SemanticError
		if !errors.As(original, &want) || !errors.As(err, &have) {
			t.Fatalf("missing native semantic cause: %v; %v", original, err)
		}
		expectEqual(t, *have, *want)
		got = mustResolve[_Target](t, t.Context(), mustJaml(t, sb, "Later: '${7}'\nOpaque: {Later: null, Now: 42}\n"))
		expectEqual(t, got.Opaque, _DetachedLazyDecoder{Now: 42, Calls: 1})
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), 7)
	})
}

type _CombinationNativeKey string

func (key *_CombinationNativeKey) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	var name string
	if err := json.UnmarshalDecode(decoder, &name); err != nil {
		return err
	}
	lazyDecodeTrace = append(lazyDecodeTrace, "native key:"+name)
	if name == "stop" {
		return errLazyDecodeStopped
	}
	*key = _CombinationNativeKey(strings.ToUpper(name))
	return nil
}

func TestJSONv2NestedMethodsKeepOrderingAndPartialValues(t *testing.T) {
	type _Entry struct {
		Known    *Expression
		Observed _OrderedLazyValue
		Rest     map[string]*Expression `json:",embed"`
	}
	type _Target []*map[_CombinationNativeKey][2]*_Entry
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, suffix := range []string{
			`,"two":[{"Known":"${21}","Observed":2},null]`,
			`,"two":[{"Known":"${21}","Observed":"stop"},null]`,
			`,"stop":[{"Known":"${21}","Observed":2},null]`,
			`,"ONE":[{"Known":"${21}","Observed":2},null]`,
		} {
			written := `[{"one":[{"Known":"${11}","a/b~c":"${12}","Observed":1},null]` + suffix + `}]`
			plain := strings.NewReplacer(`"${11}"`, "null", `"${12}"`, "null", `"${21}"`, "null").Replace(written)
			lazyDecodeTrace = nil
			var reference _Target
			original := json.Unmarshal([]byte(plain), &reference)
			calls := slices.Clone(lazyDecodeTrace)
			lazyDecodeTrace = nil
			got, err := Resolve[_Target](t.Context(), mustJaml(t, sb, written))
			expectEqual(t, lazyDecodeTrace, calls)
			if original == nil {
				if err != nil {
					t.Fatal(err)
				}
				expectEqual(t, mustResolve[int](t, t.Context(), (*got[0])["ONE"][0].Known), 11)
				expectEqual(t, mustResolve[int](t, t.Context(), (*got[0])["ONE"][0].Rest["a/b~c"]), 12)
				expectEqual(t, mustResolve[int](t, t.Context(), (*got[0])["TWO"][0].Known), 21)
				continue
			}
			expectError(t, err, CoercionFailed, original.Error())
			expectEqual(t, got, reference)
			expectEqual(t, errors.Is(err, errLazyDecodeStopped), errors.Is(original, errLazyDecodeStopped))
			expectEqual(t, errors.Is(err, jsontext.ErrDuplicateName), errors.Is(original, jsontext.ErrDuplicateName))
			var want, have *json.SemanticError
			if errors.As(original, &want) {
				if !errors.As(err, &have) {
					t.Fatal(err)
				}
				expectEqual(t, *have, *want)
			}
		}
	})
}

// The set of reflection types is finite and created once. Fuzz inputs vary
// source names and values, never the global native/JAML reflection caches.
type _JSONv2Topology struct {
	plain, lazy reflect.Type
	value       func(byte, string, byte) any
}

func jsonv2Topologies() []_JSONv2Topology {
	leaf := _JSONv2Topology{reflect.TypeFor[int](), expressionPointer, func(seed byte, _ string, _ byte) any { return int(seed) }}
	sequence := func(inner _JSONv2Topology, array bool) _JSONv2Topology {
		plain, lazy := reflect.SliceOf(inner.plain), reflect.SliceOf(inner.lazy)
		if array {
			plain, lazy = reflect.ArrayOf(2, inner.plain), reflect.ArrayOf(2, inner.lazy)
		}
		return _JSONv2Topology{plain, lazy, func(seed byte, key string, width byte) any {
			out := make([]any, int(width%4))
			for i := range out {
				out[i] = inner.value(seed+byte(i), key, width)
			}
			return out
		}}
	}
	mapping := func(inner _JSONv2Topology) _JSONv2Topology {
		return _JSONv2Topology{reflect.MapOf(reflect.TypeFor[string](), inner.plain), reflect.MapOf(reflect.TypeFor[string](), inner.lazy), func(seed byte, key string, width byte) any {
			return map[string]any{"a/b~c": inner.value(seed, key, width), key: inner.value(seed+1, key, width)}
		}}
	}
	pointer := func(inner _JSONv2Topology) _JSONv2Topology {
		return _JSONv2Topology{reflect.PointerTo(inner.plain), reflect.PointerTo(inner.lazy), inner.value}
	}
	fields := func(inner _JSONv2Topology) _JSONv2Topology {
		makeType := func(value reflect.Type) reflect.Type {
			return reflect.StructOf([]reflect.StructField{
				{Name: "Exact", Type: value, Tag: `json:"exact"`},
				{Name: "Folded", Type: value, Tag: `json:"user_name,case:ignore"`},
				{Name: "Rest", Type: reflect.MapOf(reflect.TypeFor[string](), value), Tag: `json:",embed"`},
			})
		}
		return _JSONv2Topology{makeType(inner.plain), makeType(inner.lazy), func(seed byte, key string, width byte) any {
			return map[string]any{"exact": inner.value(seed, key, width), "USER-NAME": inner.value(seed+1, key, width), key: inner.value(seed+2, key, width)}
		}}
	}
	embed := func(inner _JSONv2Topology) _JSONv2Topology {
		makeType := func(value reflect.Type) reflect.Type {
			return reflect.StructOf([]reflect.StructField{{Name: "Inner", Type: reflect.PointerTo(value), Tag: `json:",embed"`}})
		}
		return _JSONv2Topology{makeType(inner.plain), makeType(inner.lazy), inner.value}
	}
	array, slice, object := sequence(leaf, true), sequence(leaf, false), fields(leaf)
	return []_JSONv2Topology{
		leaf, array, slice, mapping(leaf), pointer(object), embed(object),
		sequence(mapping(pointer(object)), false), mapping(sequence(pointer(object), true)),
		fields(sequence(mapping(leaf), false)), embed(fields(sequence(pointer(object), true))),
		pointer(mapping(sequence(embed(object), false))), mapping(pointer(embed(fields(array)))),
	}
}

var jsonv2CombinationTypes = jsonv2Topologies()

func jsonv2ExpressionDocument(value any) string {
	switch value := value.(type) {
	case int:
		return strconv.QuoteToASCII("${" + strconv.Itoa(value) + "}")
	case []any:
		out := make([]string, len(value))
		for i, entry := range value {
			out[i] = jsonv2ExpressionDocument(entry)
		}
		return "[" + strings.Join(out, ",") + "]"
	case map[string]any:
		out := make([]string, 0, len(value))
		for _, key := range slices.Sorted(maps.Keys(value)) {
			// ASCII YAML quoting also represents valid Unicode strings whose
			// literal control characters the YAML reader would reject.
			out = append(out, strconv.QuoteToASCII(key)+":"+jsonv2ExpressionDocument(value[key]))
		}
		return "{" + strings.Join(out, ",") + "}"
	default:
		panic("unsupported combination source")
	}
}

func normalizedJSONv2Value(t *testing.T, value reflect.Value) any {
	t.Helper()
	if value.Type() == expressionPointer {
		if value.IsNil() {
			return 0
		}
		return mustResolve[int](t, t.Context(), value.Interface().(*Expression))
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return normalizedJSONv2Value(t, value.Elem())
	case reflect.Struct:
		out := make(map[string]any, value.NumField())
		for i := range value.NumField() {
			out[value.Type().Field(i).Name] = normalizedJSONv2Value(t, value.Field(i))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		out := make(map[string]any, value.Len())
		for entries := value.MapRange(); entries.Next(); {
			out[entries.Key().String()] = normalizedJSONv2Value(t, entries.Value())
		}
		return out
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		out := make([]any, value.Len())
		for i := range out {
			out[i] = normalizedJSONv2Value(t, value.Index(i))
		}
		return out
	default:
		return value.Interface()
	}
}

func checkJSONv2Combination(t *testing.T, sb *Sandbox, topology byte, key string, seed, width byte) {
	t.Helper()
	pair := jsonv2CombinationTypes[int(topology)%len(jsonv2CombinationTypes)]
	value := pair.value(seed, key, width)
	encoded, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	plain := reflect.New(pair.plain)
	original := json.Unmarshal(encoded, plain.Interface())
	written := jsonv2ExpressionDocument(value)
	lazy := reflect.New(pair.lazy)
	err = mustJaml(t, sb, written).resolve(t.Context(), lazy.Interface(), nil)
	if original == nil {
		if err != nil {
			t.Fatalf("topology %d, key %q, width %d: %v", topology, key, width, err)
		}
		expectEqual(t, normalizedJSONv2Value(t, lazy.Elem()), normalizedJSONv2Value(t, plain.Elem()))
		return
	}
	expectError(t, err, CoercionFailed)
	var want, have *json.SemanticError
	if errors.As(original, &want) {
		if !errors.As(err, &have) {
			t.Fatal(err)
		}
		expectEqual(t, have.JSONPointer, want.JSONPointer)
	}
	expectEqual(t, errors.Is(err, jsontext.ErrDuplicateName), errors.Is(original, jsontext.ErrDuplicateName))
}

func TestJSONv2NestedLazyCombinations(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for topology := range jsonv2CombinationTypes {
			for _, key := range []string{"", "0", "a/b~c", "exact", "EXACT", "user_name", "USER-NAME", "K"} {
				for width := range byte(4) {
					checkJSONv2Combination(t, sb, byte(topology), key, 17, width)
				}
			}
		}
	})
}

func FuzzJSONv2NestedLazyCombinations(f *testing.F) {
	for topology := range jsonv2CombinationTypes {
		f.Add(byte(topology), "a/b~c", byte(17), byte(2))
		f.Add(byte(topology), "user_name", byte(43), byte(1))
	}
	f.Fuzz(func(t *testing.T, topology byte, key string, seed, width byte) {
		if len(key) > 32 || !utf8.ValidString(key) {
			t.Skip()
		}
		checkJSONv2Combination(t, mustSandbox(t, trustedKind), topology, key, seed, width)
	})
}
