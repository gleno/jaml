package jaml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The callback is deliberately stateful: a supported TextUnmarshaler need not
// produce the same key when called twice with the same text.
type _StatefulLazyKey int

var statefulLazyKeyCalls int

func (k *_StatefulLazyKey) UnmarshalText([]byte) error {
	statefulLazyKeyCalls++
	*k = _StatefulLazyKey(statefulLazyKeyCalls)
	return nil
}

func TestLazyViewsDecodeStatefulMapKeysOnce(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		statefulLazyKeyCalls = 0
		var got = mustResolve[map[_StatefulLazyKey]_LazyPair](t, t.Context(), mustJaml(t, sb, "one: {a: '${11}', b: '${12}'}\ntwo: {a: '${21}'}\n"))
		expectEqual(t, statefulLazyKeyCalls, 2)
		expectEqual(t, len(got), 2)
		expectEqual(t, mustResolve[int](t, t.Context(), got[1].A), 11)
		expectEqual(t, mustResolve[int](t, t.Context(), got[1].B), 12)
		expectEqual(t, mustResolve[int](t, t.Context(), got[2].A), 21)
	})
}

func TestLazyViewsUseJSONUnicodeFieldEquivalence(t *testing.T) {
	type _Target struct{ I *Expression }
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, key := range []string{"ı", "İ"} {
			var reference struct{ I int }
			var encoded, err = json.Marshal(map[string]int{"I": 1, key: 2})
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &reference); err != nil {
				t.Fatal(err)
			}
			expectEqual(t, reference.I, 1)
			var got = mustResolve[_Target](t, t.Context(), mustJaml(t, sb, "I: '${1}'\n"+key+": '${2}'\n"))
			expectEqual(t, mustResolve[int](t, t.Context(), got.I), reference.I)
		}
	})
}

type _OrderedLazyKey string
type _OrderedLazyValue struct{ Calls int }

var lazyDecodeTrace []string
var errLazyDecodeStopped = errors.New("lazy decoding stopped")

func (k *_OrderedLazyKey) UnmarshalText(data []byte) error {
	lazyDecodeTrace = append(lazyDecodeTrace, "key:"+string(data))
	if string(data) == "stop" {
		return errLazyDecodeStopped
	}
	*k = _OrderedLazyKey(strings.ToUpper(string(data)))
	return nil
}

func (v *_OrderedLazyValue) UnmarshalJSON(data []byte) error {
	lazyDecodeTrace = append(lazyDecodeTrace, "value:"+string(data))
	v.Calls++
	if string(data) == `"stop"` {
		return errLazyDecodeStopped
	}
	return nil
}

func TestLazyViewsKeepJSONCallbackOrderingAndAborts(t *testing.T) {
	type _Entry struct {
		Later    *Expression
		Observed _OrderedLazyValue
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, test := range []struct{ document, plain string }{
			{"one: {Later: '${11}', Observed: 1}\ntwo: {Later: '${21}', Observed: 2}\n", `{"one":{"Later":null,"Observed":1},"two":{"Later":null,"Observed":2}}`},
			{"one: {Later: '${11}', Observed: 1}\ntwo: {Later: '${21}', Observed: stop}\n", `{"one":{"Later":null,"Observed":1},"two":{"Later":null,"Observed":"stop"}}`},
			{"one: {Later: '${11}', Observed: 1}\nstop: {Later: '${21}', Observed: 2}\n", `{"one":{"Later":null,"Observed":1},"stop":{"Later":null,"Observed":2}}`},
		} {
			lazyDecodeTrace = nil
			var reference map[_OrderedLazyKey]_Entry
			var expected = json.Unmarshal([]byte(test.plain), &reference)
			var calls = append([]string(nil), lazyDecodeTrace...)
			lazyDecodeTrace = nil
			got, err := Resolve[map[_OrderedLazyKey]_Entry](t.Context(), mustJaml(t, sb, test.document))
			expectEqual(t, lazyDecodeTrace, calls)
			if expected != nil {
				if !errors.Is(err, errLazyDecodeStopped) {
					t.Fatalf("got %v, want the original custom decoder error", err)
				}
				expectEqual(t, got, reference)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				expectEqual(t, mustResolve[int](t, t.Context(), got["ONE"].Later), 11)
				expectEqual(t, mustResolve[int](t, t.Context(), got["TWO"].Later), 21)
			}
		}
	})
}

func TestLazyViewsKeepIdentityThroughRecursiveTargets(t *testing.T) {
	type _Recursive struct {
		Later *Expression
		Next  map[_StatefulLazyKey]*_Recursive
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		statefulLazyKeyCalls = 0
		var got = mustResolve[_Recursive](t, t.Context(), mustJaml(t, sb, "Later: '${1}'\nNext: {one: {Later: '${2}', Next: {two: {Later: '${3}'}}}}\n"))
		// The standard decoder invokes each key callback once before its value.
		expectEqual(t, statefulLazyKeyCalls, 2)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), 1)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Next[1].Later), 2)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Next[1].Next[2].Later), 3)
	})
}

type _NativeOpaqueDecode struct {
	Hidden  *Expression
	Calls   int
	Written string
}

func (d *_NativeOpaqueDecode) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	data, err := decoder.ReadValue()
	d.Calls++
	d.Written = string(data)
	d.Hidden = new(Expression)
	return err
}

func TestLazyViewsPreserveNativeCustomDecoderBoundary(t *testing.T) {
	type _Target struct {
		Later  *Expression
		Opaque _NativeOpaqueDecode
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var reference _Target
		if err := json.Unmarshal([]byte(`{"Later":null,"Opaque":{"Hidden":2}}`), &reference); err != nil {
			t.Fatal(err)
		}
		var got = mustResolve[_Target](t, t.Context(), mustJaml(t, sb, "Later: '${1}'\nOpaque: {Hidden: '${2}'}\n"))
		expectEqual(t, got.Opaque, reference.Opaque)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), 1)
	})
}

type _StrictNativeDecode struct {
	Hidden *Expression
}

func (d *_StrictNativeDecode) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	type _Fields _StrictNativeDecode
	return json.UnmarshalDecode(decoder, (*_Fields)(d), json.RejectUnknownMembers(true))
}

func TestLazyViewsPreserveDelegatedDecoderOptions(t *testing.T) {
	type _Target struct {
		Later  *Expression
		Opaque _StrictNativeDecode
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var reference _Target
		var original = json.Unmarshal([]byte(`{"Later":null,"Opaque":{"Hidden":{"unexpected":2}}}`), &reference)
		if original == nil {
			t.Fatal("reference decoder accepted an unknown field")
		}
		got, err := Resolve[_Target](t.Context(), mustJaml(t, sb, "Later: '${1}'\nOpaque: {Hidden: {unexpected: 2}}\n"))
		expectError(t, err, CoercionFailed, original.Error())
		expectEqual(t, got, reference)
	})
}

func TestLazyViewsRetainRootArrayAndEscapedKeyPaths(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var root = mustResolve[*Expression](t, t.Context(), mustJaml(t, sb, "'${7}'"))
		expectEqual(t, mustResolve[int](t, t.Context(), root), 7)
		var entries = mustResolve[map[string][]*Expression](t, t.Context(), mustJaml(t, sb, "'a/b~c': ['${11}', '${12}']\n'': ['${21}', '${22}']\n"))
		for key, values := range map[string][]int{"a/b~c": {11, 12}, "": {21, 22}} {
			for index, expected := range values {
				expectEqual(t, mustResolve[int](t, t.Context(), entries[key][index]), expected)
			}
		}
	})
}

func FuzzLazyFieldSelectionMatchesJSONv2(f *testing.F) {
	for _, key := range []string{"I", "i", "ı", "İ", "unknown", "I\x00", "\x7f", "K"} {
		f.Add(key)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if len(key) > 128 || !utf8.ValidString(key) {
			t.Skip()
		}
		var plain, _ = json.Marshal(map[string]int{"I": 1, key: 2})
		var reference struct{ I int }
		if err := json.Unmarshal(plain, &reference); err != nil {
			t.Fatal(err)
		}
		// Quote every key as ASCII YAML so control and non-BMP characters do not
		// turn the parser's text restrictions into unrelated fuzz failures.
		var source string
		for _, entry := range []struct{ key, expression string }{{"I", "${1}"}, {key, "${2}"}} {
			if entry.key == "I" && entry.expression == "${1}" && key == "I" {
				continue
			}
			source += strconv.QuoteToASCII(entry.key) + ": '" + entry.expression + "'\n"
		}
		var sb, err = MakeTrustedSandbox(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var got = mustResolve[struct{ I *Expression }](t, t.Context(), mustJaml(t, sb, source))
		expectEqual(t, mustResolve[int](t, t.Context(), got.I), reference.I)
	})
}
