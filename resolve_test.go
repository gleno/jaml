package jaml

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestResolveWalksTheDocumentInWrittenOrder(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
zebra: ${ tick('zebra') }
apple:
  - ${ tick('apple 0') }
  - inner: ${ tick('inner') }
    first: ${ tick('first') }
  - "${{ tick('left') }} ${{ tick('right') }}"
shared: &shared ${ tick('alias') }
again: *shared
mango: ${ seen }
`)
		var ctx = WithScript(t.Context(), "const order = []; function tick(name) { order.push(name); return order.length } var seen = () => order")
		var got = mustResolve[map[string]any](t, ctx, document)
		expectEqual(t, got["mango"], []any{"zebra", "apple 0", "inner", "first", "left", "right", "alias", "alias"})
	})
}

func TestAFunctionLeafIsCalledWithTheArguments(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
rename: "${ (state, suffix) => ({ ...state, name: state.name + suffix }) }"
thunk: ${ (u) => () => () => u.id * 2 }
plain: ${ 7 }
receiver: ${ function () { return this === globalThis } }
`)
		type _State struct {
			Name string `json:"name"`
			Age  int    `json:"age"`
		}
		expectEqual(t, mustResolve[_State](t, t.Context(), document.At("rename"), _State{Name: "bob", Age: 3}, "!"), _State{Name: "bob!", Age: 3})
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("thunk"), map[string]any{"id": 21}), 42)
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("plain"), "ignored"), 7)
		expectEqual(t, mustResolve[bool](t, t.Context(), document.At("receiver")), true)
	})
}

func TestAnExpressionFieldStaysUnevaluatedUntilItsOwnResolve(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
config:
  team: ${ user.team }
  names: ${ (u) => [u.name, user.team] }
  later:
    a: ${ (u) => u.name + 'a' }
    b: ${ (u) => u.name + 'b' }
  each:
    - ${ (u) => 1 }
    - ${ (u) => u.name }
`)
		type _Config struct {
			Team  string                 `json:"team"`
			Names *Expression            `json:"names"`
			Later map[string]*Expression `json:"later"`
			Each  []*Expression          `json:"each"`
		}
		var ctx = withValues(t, map[string]any{"user": map[string]any{"team": "core"}})
		var got = mustResolve[_Config](t, ctx, document.At("config"))
		expectEqual(t, got.Team, "core")
		var other = map[string]any{"name": "ada"}
		expectEqual(t, mustResolve[[]string](t, ctx, got.Names, other), []string{"ada", "core"})
		expectEqual(t, mustResolve[string](t, ctx, got.Later["b"], other), "adab")
		expectEqual(t, mustResolve[string](t, ctx, got.Each[1], other), "ada")
		var numbered = mustResolve[map[int]*Expression](t, ctx, mustJaml(t, sb, "\"1\": \"${ (u) => u.name }\"\n"))
		expectEqual(t, mustResolve[string](t, ctx, numbered[1], other), "ada")
	})
}

func TestAnExpressionFieldBindsWhereverTheDocumentHasThePath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "present: ~\nNamed: ${ 'folded' }\n")
		type _Target struct {
			Present *Expression `json:"present"`
			Missing *Expression `json:"missing"`
			Named   *Expression `json:"named"`
		}
		var got = mustResolve[_Target](t, t.Context(), document)
		if got.Missing != nil {
			t.Fatalf("a path the document lacks bound %v", got.Missing)
		}
		expectEqual(t, mustResolve[any](t, t.Context(), got.Present), nil)
		expectEqual(t, mustResolve[string](t, t.Context(), got.Named), "folded")
	})
}

type _Observing struct {
	raw string
}

func (o *_Observing) UnmarshalJSON(written []byte) error {
	o.raw = string(written)
	return nil
}

type _Observed struct {
	Later *Expression `json:"later"`
	Rest  _Observing  `json:"rest"`
}

func TestTheTargetNeverObservesAPlaceholder(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "later: ${ (x) => x + 1 }\nrest:\n  inner: ${ (x) => x * 10 }\n")
		var got = mustResolve[_Observed](t, t.Context(), document, 4)
		expectEqual(t, got.Rest.raw, `{"inner":40}`)
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later, 1), 2)
		type _Raw struct {
			Later json.RawMessage `json:"later"`
		}
		expectEqual(t, string(mustResolve[_Raw](t, t.Context(), document, 4).Later), "5")
		expectEqual(t, mustResolve[map[string]any](t, t.Context(), document, 4), map[string]any{"later": float64(5), "rest": map[string]any{"inner": float64(40)}})
	})
}

func TestACopyOutlivesTheContextItWasMadeWith(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		made, end := context.WithCancel(t.Context())
		document, err := sb.MakeExpressionFromJaml(made, "config:\n  v: ${ 1 + 1 }\n")
		if err != nil {
			t.Fatal(err)
		}
		kept, err := document.At("config").Copy(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		end()
		_, err = Resolve[int](t.Context(), document.At("config", "v"))
		expectError(t, err, Closed)
		expectEqual(t, mustResolve[map[string]int](t, t.Context(), kept), map[string]int{"v": 2})
		expectEqual(t, mustResolve[int](t, t.Context(), kept.At("v")), 2)
		_, err = document.Copy(t.Context())
		expectError(t, err, Closed)
	})
}

func TestACopyDiesWithItsSandbox(t *testing.T) {
	for _, kind := range sandboxKinds {
		t.Run(kind.name, func(t *testing.T) {
			if kind.name == isolatedKind.name && isolationSupported() != nil {
				t.Skip(isolationSupported())
			}
			lifetime, end := context.WithCancel(t.Context())
			sb, err := kind.make(lifetime)
			if err != nil {
				t.Fatal(err)
			}
			kept, err := mustJaml(t, sb, "v: 1\n").Copy(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			end()
			_, err = Resolve[map[string]int](t.Context(), kept)
			expectError(t, err, Closed)
		})
	}
}

func TestValuesMergeByNameAndAreCopiedWhenBound(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var pair = mustExpression(t, sb, "[a, b]")
		var values = map[string]any{"a": 1, "b": 2}
		var ctx = WithValues(t.Context(), values)
		ctx = WithValues(ctx, map[string]any{"b": 3})
		values["a"] = 9
		expectEqual(t, mustResolve[[]int](t, ctx, pair), []int{1, 3})
		expectEqual(t, mustResolve[[]int](t, WithValues(t.Context(), values), pair), []int{9, 2})
	})
}

func TestEveryLeafSeesTheValuesAsBound(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
replace: ${ (() => { try { user = 1 } catch (e) { return e.name } })() }
remove: ${ (() => { try { delete globalThis.user } catch (e) { return e.name } })() }
deep: ${ (() => { try { user.tags.push('x') } catch (e) { return e.name } })() }
seen: ${ [user.name, user.tags.length, this.user === user] }
`)
		var ctx = withValues(t, map[string]any{"user": map[string]any{"name": "ada", "tags": []string{"a"}}})
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{
			"replace": "TypeError",
			"remove":  "TypeError",
			"deep":    "TypeError",
			"seen":    []any{"ada", float64(1), true},
		})
	})
}

func TestTheUnwrapCapCountsEveryCall(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var chain = func(calls int) string {
			return "x => " + strings.Repeat("() => ", calls-1) + "x"
		}
		expectEqual(t, mustResolve[int](t, t.Context(), mustExpression(t, sb, chain(16)), 7), 7)
		_, err := Resolve[int](t.Context(), mustExpression(t, sb, chain(17)), 7)
		expectError(t, err, EvaluationFailed, "$: ", "still a function after 16 calls")
		var document = mustJaml(t, sb, "deep:\n  v: \"${ "+chain(17)+" }\"\n")
		_, err = Resolve[any](t.Context(), document)
		expectError(t, err, EvaluationFailed, "$.deep.v: ", "still a function after 16 calls")
	})
}

func TestMergedKeysAreWalkedWhereTheMergeIsWritten(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
base: &base
  b: ${ tick('base b') }
  c: ${ tick('base c') }
other: &other
  d: ${ tick('other d') }
m:
  a: ${ tick('own a') }
  <<: [*base, *other]
  c: ${ tick('own c') }
  z: ${ seen }
`)
		var ctx = WithScript(t.Context(), "const order = []; function tick(name) { order.push(name); return order.length } var seen = () => order")
		var got = mustResolve[map[string]any](t, ctx, document.At("m"))
		expectEqual(t, got["z"], []any{"own a", "base b", "other d", "own c"})
	})
}

type _LazyHeld struct {
	Limit *Expression `json:"limit"`
}

type _LazyTarget struct {
	Root  _LazyHeld              `json:"root"`
	Named map[string]*Expression `json:"named"`
	Each  []*Expression          `json:"each"`
	Deep  []map[string]_LazyHeld `json:"deep"`
	Whole *_LazyTarget           `json:"whole"`
}

func unmadeExpressions(value reflect.Value, path string) []string {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if held, isExpression := value.Interface().(*Expression); isExpression {
			if held.held == nil {
				return []string{path}
			}
			return nil
		}
		return unmadeExpressions(value.Elem(), path)
	case reflect.Struct:
		var found []string
		for i := range value.NumField() {
			found = append(found, unmadeExpressions(value.Field(i), path+"."+value.Type().Field(i).Name)...)
		}
		return found
	case reflect.Map:
		var found []string
		for _, key := range value.MapKeys() {
			found = append(found, unmadeExpressions(value.MapIndex(key), fmt.Sprintf("%s[%v]", path, key))...)
		}
		return found
	case reflect.Slice, reflect.Array:
		var found []string
		for i := range value.Len() {
			found = append(found, unmadeExpressions(value.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return found
	}
	return nil
}

func TestCodeNeverFillsALazyField(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var refused = map[string]string{
			"root.limit: object":      "root: \"${ {limit: {}} }\"\n",
			"root.LIMIT: folded key":  "root: \"${ {LIMIT: 5} }\"\n",
			"named.a: map value":      "named: \"${ {a: {}} }\"\n",
			"each[1]: slice item":     "each: \"${ [null, 'x'] }\"\n",
			"deep[0].k.limit: nested": "deep:\n  - \"${ {k: {limit: []}} }\"\n",
			"whole.named.a: whole":    "\"${ {whole: {named: {a: false}}} }\"\n",
			"whole.root.limit: zero":  "whole: \"${ {root: {limit: 0}} }\"\n",
		}
		for name, text := range refused {
			t.Run(name, func(t *testing.T) {
				var place, _, _ = strings.Cut(name, ":")
				_, err := Resolve[_LazyTarget](t.Context(), mustJaml(t, sb, text))
				expectError(t, err, CoercionFailed, "$."+place+": code produced a value where Resolve expects a *jaml.Expression")
			})
		}
		var accepted = []string{
			"root: \"${ {limit: null} }\"\n",
			"root: \"${ {} }\"\n",
			"named: \"${ {a: null} }\"\n",
			"each: \"${ [null] }\"\n",
			"deep:\n  - \"${ {k: {limit: null}} }\"\n",
			"whole: \"${ {root: {}, named: {}, each: []} }\"\n",
			"root: {limit: ~}\nnamed: {a: ~}\neach: [~]\ndeep: [{k: {limit: ~}}]\nwhole: {whole: {root: {limit: \"${ 1 }\"}}}\n",
		}
		for _, text := range accepted {
			got, err := Resolve[_LazyTarget](t.Context(), mustJaml(t, sb, text))
			if err != nil {
				t.Fatalf("%q: %v", text, err)
			}
			if unmade := unmadeExpressions(reflect.ValueOf(got), "T"); len(unmade) > 0 {
				t.Fatalf("%q: T observes unmade expressions at %v", text, unmade)
			}
		}
		var codeNull = mustResolve[_LazyTarget](t, t.Context(), mustJaml(t, sb, "root: \"${ {limit: null} }\"\n"))
		if codeNull.Root.Limit != nil {
			t.Fatalf("code null bound %v", codeNull.Root.Limit)
		}
		var documentNull = mustResolve[_LazyTarget](t, t.Context(), mustJaml(t, sb, "root: {limit: ~}\n"))
		expectEqual(t, mustResolve[any](t, t.Context(), documentNull.Root.Limit), nil)
	})
}
