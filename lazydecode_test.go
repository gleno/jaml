package jaml

import (
	"encoding/json"
	"errors"
	"testing"
)

type _LazyPair struct {
	A *Expression
	B *Expression
}

type _LazyContainers struct {
	Map     map[string]_LazyPair
	Pointer *_LazyPair
	Array   [2]_LazyPair
	Slice   []_LazyPair
	Value   _LazyPair
}

func TestLazyViewsFollowJSONContainerReplacement(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for name, test := range map[string]struct {
			text  string
			check func(*testing.T, _LazyContainers)
		}{
			"cleared map": {
				"map: {k: {a: '${1}'}}\nMAP: null\n",
				func(t *testing.T, got _LazyContainers) { expectEqual(t, got.Map, map[string]_LazyPair(nil)) },
			},
			"cleared pointer": {
				"pointer: {a: '${1}'}\nPOINTER: null\n",
				func(t *testing.T, got _LazyContainers) { expectEqual(t, got.Pointer, (*_LazyPair)(nil)) },
			},
			"replaced map entry": {
				"map: {k: {a: '${1}'}}\nMAP: {k: {b: '${2}'}}\n",
				func(t *testing.T, got _LazyContainers) {
					expectEqual(t, got.Map["k"].A, (*Expression)(nil))
					expectEqual(t, mustResolve[int](t, t.Context(), got.Map["k"].B), 2)
				},
			},
			"array tail zeroed": {
				"array: [{a: '${1}'}, {a: '${2}'}]\nARRAY: [{b: '${3}'}]\n",
				func(t *testing.T, got _LazyContainers) {
					expectEqual(t, got.Array[1], _LazyPair{})
					expectEqual(t, mustResolve[int](t, t.Context(), got.Array[0].A), 1)
					expectEqual(t, mustResolve[int](t, t.Context(), got.Array[0].B), 3)
				},
			},
			"slice truncates and merges": {
				"slice: [{a: '${1}'}, {a: '${2}'}]\nSLICE: [{b: '${3}'}]\n",
				func(t *testing.T, got _LazyContainers) {
					expectEqual(t, len(got.Slice), 1)
					expectEqual(t, mustResolve[int](t, t.Context(), got.Slice[0].A), 1)
					expectEqual(t, mustResolve[int](t, t.Context(), got.Slice[0].B), 3)
				},
			},
			"struct merges": {
				"value: {a: '${1}'}\nVALUE: {b: '${2}'}\n",
				func(t *testing.T, got _LazyContainers) {
					expectEqual(t, mustResolve[int](t, t.Context(), got.Value.A), 1)
					expectEqual(t, mustResolve[int](t, t.Context(), got.Value.B), 2)
				},
			},
			"code null replaces a view": {
				"value: {a: '${1}'}\nVALUE: '${ ({a: null}) }'\n",
				func(t *testing.T, got _LazyContainers) { expectEqual(t, got.Value.A, (*Expression)(nil)) },
			},
		} {
			t.Run(name, func(t *testing.T) {
				defer func() {
					if failure := recover(); failure != nil {
						t.Errorf("Resolve panicked: %v", failure)
					}
				}()
				test.check(t, mustResolve[_LazyContainers](t, t.Context(), mustJaml(t, sb, test.text)))
			})
		}
	})
}

func TestLazyViewsFollowDecodedMapKeyReplacement(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[map[int]_LazyPair](t, t.Context(), mustJaml(t, sb, `"1": {a: '${1}'}
"01": {b: '${2}'}
`))
		expectEqual(t, len(got), 1)
		expectEqual(t, got[1].A, (*Expression)(nil))
		expectEqual(t, mustResolve[int](t, t.Context(), got[1].B), 2)
		var leaf = mustResolve[map[int]*Expression](t, t.Context(), mustJaml(t, sb, "\"1\": '${1}'\n\"01\": '${2}'\n"))
		expectEqual(t, mustResolve[int](t, t.Context(), leaf[1]), 2)
		var named = mustResolve[map[_Hue]_LazyPair](t, t.Context(), mustJaml(t, sb, "red: {a: '${1}'}\nRED: {b: '${2}'}\n"))
		expectEqual(t, named["RED"].A, (*Expression)(nil))
		expectEqual(t, mustResolve[int](t, t.Context(), named["RED"].B), 2)
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
		var mismatch *json.UnmarshalTypeError
		if !errors.As(err, &mismatch) {
			t.Fatalf("missing JSON mismatch: %v", err)
		}
		var expected _Decoded
		var original = json.Unmarshal([]byte(`{"Later":null,"Nested":{"Port":"bad"}}`), &expected)
		var reference *json.UnmarshalTypeError
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
			"type error continues": {
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
				var reference, mismatch *json.UnmarshalTypeError
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
