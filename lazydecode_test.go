package jaml

import (
	"encoding/json/v2"
	"errors"
	"testing"
)

type _LazyPair struct {
	A *Expression `json:"a"`
	B *Expression `json:"b"`
}

type _LazyContainers struct {
	Map     map[string]_LazyPair `json:"map"`
	Pointer *_LazyPair           `json:"pointer"`
	Array   [2]_LazyPair         `json:"array"`
	Slice   []_LazyPair          `json:"slice"`
	Value   _LazyPair            `json:"value"`
}

func TestLazyViewsFollowJSONContainerReplacement(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var initial = mustJaml(t, sb, "map: {k: {a: '${1}'}}\npointer: {a: '${1}'}\narray: [{a: '${1}'}, {a: '${2}'}]\nslice: [{a: '${1}'}, {a: '${2}'}]\nvalue: {a: '${1}'}\n")
		var got = mustResolve[_LazyContainers](t, t.Context(), initial)
		var update = mustJaml(t, sb, "map: {k: {b: '${3}'}}\npointer: null\narray: [{b: '${3}'}, {}]\nslice: [{b: '${3}'}]\nvalue: {b: '${3}'}\n")
		if err := update.decodeInto(t.Context(), &got, nil); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, mustResolve[int](t, t.Context(), got.Map["k"].A), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Map["k"].B), 3)
		expectEqual(t, got.Pointer, (*_LazyPair)(nil))
		expectEqual(t, got.Array[0].A, (*Expression)(nil))
		expectEqual(t, got.Array[1], _LazyPair{})
		expectEqual(t, mustResolve[int](t, t.Context(), got.Array[0].B), 3)
		expectEqual(t, len(got.Slice), 1)
		expectEqual(t, got.Slice[0].A, (*Expression)(nil))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Slice[0].B), 3)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Value.A), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Value.B), 3)
	})
}

func TestLazyViewsRejectDecodedMapKeyCollisions(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got, err := Resolve[map[_Hue]_LazyPair](t.Context(), mustJaml(t, sb, "red: {a: '${1}'}\nRED: {b: '${2}'}\n"))
		expectError(t, err, CoercionFailed, "duplicate")
		expectEqual(t, got, map[_Hue]_LazyPair{"RED": {}})
		leaf, err := Resolve[map[int]*Expression](t.Context(), mustJaml(t, sb, "\"0\": '${1}'\n\"-0\": '${2}'\n"))
		expectError(t, err, CoercionFailed, "duplicate")
		expectEqual(t, leaf, map[int]*Expression{0: nil})
	})
}

type LazyEmbeddedView struct {
	Later *Expression
}

func TestLazyViewsBindInsideEmbeddedMapValues(t *testing.T) {
	type _Embedded struct{ *LazyEmbeddedView }
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[map[string]_Embedded](t, t.Context(), mustJaml(t, sb, "entry: {Later: '${7}'}\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), got["entry"].Later), 7)
	})
}

type _ObservedDecode struct {
	Calls   int
	Written string
}

func (d *_ObservedDecode) UnmarshalJSON(written []byte) error {
	d.Calls++
	d.Written = string(written)
	return nil
}

func TestLazyViewsKeepStandardJSONDecoding(t *testing.T) {
	type _Decoded struct {
		Later  *Expression
		Number int `json:",string"`
		Custom _ObservedDecode
		Nested struct{ Port int }
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[_Decoded](t, t.Context(), mustJaml(t, sb, "Later: '${1}'\nNumber: '42'\nCustom: '${ ({a: 2}) }'\n"))
		expectEqual(t, got.Number, 42)
		expectEqual(t, got.Custom, _ObservedDecode{Calls: 1, Written: `{"a":2}`})
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), 1)
		var _, err = Resolve[_Decoded](t.Context(), mustJaml(t, sb, "Later: '${1}'\nNested: {Port: bad}\n"))
		expectError(t, err, CoercionFailed, "$.Nested.Port")
		var mismatch *json.SemanticError
		if !errors.As(err, &mismatch) {
			t.Fatalf("missing JSON mismatch: %v", err)
		}
		var expected _Decoded
		var original = json.Unmarshal([]byte(`{"Later":null,"Nested":{"Port":"bad"}}`), &expected)
		var reference *json.SemanticError
		if !errors.As(original, &reference) {
			t.Fatal(original)
		}
		expectEqual(t, *mismatch, *reference)
		_, err = Resolve[_Decoded](t.Context(), mustJaml(t, sb, "Later: '${1}'\nNumber: 42\n"))
		original = json.Unmarshal([]byte(`{"Later":null,"Number":42}`), &expected)
		if original == nil {
			t.Fatal("encoding/json accepted an unquoted number for a ,string field")
		}
		expectError(t, err, CoercionFailed, original.Error())
	})
}

type _AbortingDecode struct {
	Calls  int
	Hidden *Expression
}

func (d *_AbortingDecode) UnmarshalJSON([]byte) error {
	d.Calls++
	d.Hidden = new(Expression)
	return errors.New("stop decoding")
}

func TestLazyViewsPreservePartialResultsOnDecodeFailure(t *testing.T) {
	type _Partial struct {
		Later   *Expression
		Map     map[_Hue]_LazyPair
		Slice   []_LazyPair
		Array   [2]_LazyPair
		Pointer *_LazyPair
		Number  int
		Custom  _AbortingDecode
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for name, test := range map[string]struct{ document, json string }{
			"type error stops immediately": {
				"Later: '${1}'\nNumber: bad\nMap: {red: {a: '${2}'}}\nSlice: [{a: '${3}'}]\nArray: [{b: '${4}'}]\nPointer: {a: '${5}'}\n",
				`{"Later":null,"Number":"bad","Map":{"red":{"a":null}},"Slice":[{"a":null}],"Array":[{"b":null}],"Pointer":{"a":null}}`,
			},
			"custom decoder error": {
				"Later: '${1}'\nMap: {red: {a: '${2}'}}\nCustom: 0\nPointer: {a: '${3}'}\n",
				`{"Later":null,"Map":{"red":{"a":null}},"Custom":0,"Pointer":{"a":null}}`,
			},
		} {
			t.Run(name, func(t *testing.T) {
				var expected _Partial
				var original = json.Unmarshal([]byte(test.json), &expected)
				if original == nil {
					t.Fatal("reference decode did not fail")
				}
				got, err := Resolve[_Partial](t.Context(), mustJaml(t, sb, test.document))
				expectError(t, err, CoercionFailed, original.Error())
				expectEqual(t, got, expected)
				var reference, mismatch *json.SemanticError
				if errors.As(original, &reference) {
					if !errors.As(err, &mismatch) {
						t.Fatalf("missing JSON mismatch: %v", err)
					}
					expectEqual(t, *mismatch, *reference)
				}
			})
		}
	})
}
