package jaml

import (
	"context"
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"unicode/utf8"
)

type NativeFoldedLazy struct {
	Later *Expression `json:"name,case:ignore"`
}

type NativeFoldedPlain struct {
	Later int `json:"name,case:ignore"`
}

func TestNativeFieldsRejectAmbiguousFoldedMatches(t *testing.T) {
	type _Plain struct {
		NativeFoldedPlain
		Now int `json:"NAME,case:ignore"`
	}
	type _Lazy struct {
		NativeFoldedLazy
		Now int `json:"NAME,case:ignore"`
	}
	var reference _Plain
	var original = json.Unmarshal([]byte(`{"NaMe":7}`), &reference)
	if original == nil {
		t.Fatal("native decoder accepted an ambiguous name")
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := Resolve[_Lazy](t.Context(), mustJaml(t, sb, "NaMe: '${7}'\n"))
		expectError(t, err, CoercionFailed, "ambiguous")
	})
}

func TestNativeFieldsRespectStrictCaseBeforeFallback(t *testing.T) {
	type _Plain struct {
		Later int `json:"name,case:strict"`
		Now   int `json:"NAME,case:ignore"`
	}
	type _Lazy struct {
		Later *Expression `json:"name,case:strict"`
		Now   int         `json:"NAME,case:ignore"`
	}
	var reference _Plain
	if err := json.Unmarshal([]byte(`{"Name":7}`), &reference); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, reference.Now, 7)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Lazy](t, t.Context(), mustJaml(t, sb, "Name: '${7}'\n"))
		expectEqual(t, got.Now, reference.Now)
		expectEqual(t, got.Later, (*Expression)(nil))
	})
}

func TestNativeFieldsRetainUnicodeTagNames(t *testing.T) {
	type _Plain struct {
		Later int `json:"🦊"`
	}
	type _Lazy struct {
		Later *Expression `json:"🦊"`
	}
	var reference _Plain
	if err := json.Unmarshal([]byte(`{"🦊":7}`), &reference); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, reference.Later, 7)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Lazy](t, t.Context(), mustJaml(t, sb, "🦊: '${7}'\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), reference.Later)
	})
}

func TestNativeFieldsFlattenExplicitEmbeddedStructs(t *testing.T) {
	type _Plain struct {
		Inner struct{ Later int } `json:",embed"`
	}
	type _Lazy struct {
		Inner struct{ Later *Expression } `json:",embed"`
	}
	var reference _Plain
	if err := json.Unmarshal([]byte(`{"Later":7}`), &reference); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, reference.Inner.Later, 7)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Lazy](t, t.Context(), mustJaml(t, sb, "Later: '${7}'\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Inner.Later), reference.Inner.Later)
	})
}

func TestNativeFieldsBindEmbeddedFallbackMaps(t *testing.T) {
	type _Plain struct {
		Known int            `json:",case:strict"`
		Rest  map[string]int `json:",embed"`
	}
	type _Lazy struct {
		Known int                    `json:",case:strict"`
		Rest  map[string]*Expression `json:",embed"`
	}
	var reference _Plain
	if err := json.Unmarshal([]byte(`{"Known":1,"known":7,"unknown":8}`), &reference); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, reference.Known, 1)
	expectEqual(t, reference.Rest, map[string]int{"known": 7, "unknown": 8})
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Lazy](t, t.Context(), mustJaml(t, sb, "Known: 1\nknown: '${7}'\nunknown: '${8}'\n"))
		expectEqual(t, got.Known, reference.Known)
		expectEqual(t, len(got.Rest), len(reference.Rest))
		for key, expected := range reference.Rest {
			expectEqual(t, mustResolve[int](t, t.Context(), got.Rest[key]), expected)
		}
	})
}

func TestNativeFieldsKeepRecursiveFallbackIdentity(t *testing.T) {
	type _Recursive struct {
		Value *Expression
		Rest  map[string]*_Recursive `json:",embed"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Recursive](t, t.Context(), mustJaml(t, sb, "Value: '${1}'\nchild: {Value: '${2}', next: {Value: '${3}'}}\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Value), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Rest["child"].Value), 2)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Rest["child"].Rest["next"].Value), 3)
	})
}

func TestNativeFieldsKeepFallbackPathsAcrossCopy(t *testing.T) {
	type _Lazy struct {
		Known int
		Rest  *map[string]*Expression `json:",embed"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		made, end := context.WithCancel(t.Context())
		defer end()
		document, err := sb.MakeExpressionFromJaml(made, "outer: {Known: 1, 'a/b~c': '${7}'}\n")
		if err != nil {
			t.Fatal(err)
		}
		kept, err := document.At("outer").Copy(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		end()
		var got = mustResolve[_Lazy](t, t.Context(), kept)
		expectEqual(t, got.Known, 1)
		expectEqual(t, len(*got.Rest), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), (*got.Rest)["a/b~c"]), 7)
	})
}

func TestNativeFieldsClearFallbackPlaceholdersOnFailure(t *testing.T) {
	type _Partial struct {
		Number int
		Rest   map[string]_LazyPair `json:",embed"`
	}
	var reference _Partial
	var original = json.Unmarshal([]byte(`{"extra":{"a":null},"Number":"bad","other":{"b":null}}`), &reference)
	var expected *json.SemanticError
	if !errors.As(original, &expected) {
		t.Fatal(original)
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got, err := Resolve[_Partial](t.Context(), mustJaml(t, sb, "extra: {a: '${1}'}\nNumber: bad\nother: {b: '${2}'}\n"))
		var mismatch *json.SemanticError
		if !errors.As(err, &mismatch) {
			t.Fatal(err)
		}
		expectEqual(t, *mismatch, *expected)
		expectEqual(t, got, reference)
	})
}

type _NativeEmbedKey string

func (k _NativeEmbedKey) AppendText(into []byte) ([]byte, error) {
	return append(into, string(k)...), nil
}

func TestNativeFieldsRejectInvalidEmbedsBeforeEvaluation(t *testing.T) {
	type _Lazy struct {
		Named struct{ Later *Expression }     `json:"named,embed"`
		Rest  map[_NativeEmbedKey]*Expression `json:",embed"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := Resolve[_Lazy](t.Context(), mustExpression(t, sb, "(() => { throw 'ran' })()"))
		expectError(t, err, InvalidTarget, "cannot have any options other than")
	})
}

func TestNativeFieldsRejectMalformedInputNames(t *testing.T) {
	var kind = reflect.StructOf([]reflect.StructField{
		{Name: "Bad", Type: reflect.TypeFor[string](), Tag: reflect.StructTag("json:" + strconv.Quote("a\xff"))},
		{Name: "Good", Type: reflect.TypeFor[string](), Tag: reflect.StructTag("json:" + strconv.Quote("a�"))},
	})
	var value = reflect.New(kind).Elem()
	value.Field(0).SetString("bad")
	value.Field(1).SetString("good")
	_, err := json.Marshal(value.Interface())
	if err == nil {
		t.Fatal("native codec accepted malformed input metadata")
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var ctx = WithValues(t.Context(), map[string]any{"value": value.Interface()})
		_, err := Resolve[string](ctx, mustExpression(t, sb, "JSON.stringify(value)"))
		expectError(t, err, InvalidInput)
	})
}

type _NativeFallbackWithMethod map[string]_Misnamed

var nativeFallbackMethodCalls int

func (_NativeFallbackWithMethod) MarshalJSON() ([]byte, error) {
	nativeFallbackMethodCalls++
	return []byte("{}"), nil
}

func TestNativeFieldsRejectMethodsOnEmbeddedFallbacks(t *testing.T) {
	type _Input struct {
		Rest _NativeFallbackWithMethod `json:",embed"`
	}
	var input = _Input{Rest: _NativeFallbackWithMethod{"entry": {Label: "fine"}}}
	nativeFallbackMethodCalls = 0
	_, err := json.Marshal(input)
	if err == nil {
		t.Fatal("native codec accepted an embedded fallback with methods")
	}
	expectEqual(t, nativeFallbackMethodCalls, 0)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var ctx = WithValues(t.Context(), map[string]any{"value": input})
		_, err := Resolve[any](ctx, mustExpression(t, sb, "value"))
		expectError(t, err, InvalidInput, "must not implement marshal or unmarshal methods")
		expectEqual(t, nativeFallbackMethodCalls, 0)
	})
}

func TestNativeFieldsPreserveFallbackValueCallbacks(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var calls int
		nativeFallbackMethodCalls = 0
		var rest = map[string]_InputText{"entry": {calls: &calls}}
		var input = struct {
			Rest *map[string]_InputText `json:",embed"`
		}{Rest: &rest}
		var ctx = WithValues(t.Context(), map[string]any{"value": input})
		expectEqual(t, mustResolve[map[string]string](t, ctx, mustExpression(t, sb, "value")), map[string]string{"entry": "1"})
		expectEqual(t, calls, 1)
		expectEqual(t, nativeFallbackMethodCalls, 0)
	})
}

func FuzzNativeFieldSelectionMatchesJSONv2(f *testing.F) {
	var tags = []string{"", "name", "name,case:strict", "name,case:ignore", "name,case:strict,case:ignore", "🦊", "a\x7f", "a\xff", "-", "-,", "name,embed", ",embed", ",embed,omitempty", "name'bad", "'comma,name'", "name,unknown", "name,case:unknown", "name,format:'x,y',case:strict"}
	var targets []reflect.Type
	for _, tag := range tags {
		targets = append(targets, reflect.StructOf([]reflect.StructField{
			{Name: "First", Type: reflect.TypeFor[int](), Tag: reflect.StructTag("json:" + strconv.Quote(tag))},
			{Name: "Second", Type: reflect.TypeFor[int](), Tag: `json:"NAME,case:ignore"`},
		}))
	}
	for index := range tags {
		for _, key := range []string{"First", "name", "Name", "NAME", "🦊", "a�", "-"} {
			f.Add(uint(index), key)
		}
	}
	f.Fuzz(func(t *testing.T, index uint, key string) {
		if len(key) > 128 || !utf8.ValidString(key) {
			t.Skip()
		}
		var target = targets[index%uint(len(targets))]
		var pointer = reflect.New(target)
		var encoded, _ = json.Marshal(map[string]int{key: 7})
		if err := json.Unmarshal(encoded, pointer.Interface()); err != nil {
			// Unsupported format tags can reject a whole type before matching.
			t.Skip()
		}
		var expected = setLeaf(pointer.Elem(), nil)
		got, found := fieldFor(target, key)
		if !found {
			got = nil
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("%s key %q: picked %v, native picked %v", target, key, got, expected)
		}
	})
}
