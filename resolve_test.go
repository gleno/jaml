package jaml

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveWalksTheDocumentInWrittenOrder(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
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
		ctx := WithScript(t.Context(), "const order = []; function tick(name) { order.push(name); return order.length } var seen = () => order")
		got := mustResolve[map[string]any](t, ctx, document)
		expectEqual(t, got["mango"], []any{"zebra", "apple 0", "inner", "first", "left", "right", "alias", "alias"})
	})
}

func TestAFunctionLeafIsCalledWithTheArguments(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
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
		document := mustJaml(t, sb, `
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
		ctx := withValues(t, map[string]any{"user": map[string]any{"team": "core"}})
		got := mustResolve[_Config](t, ctx, document.At("config"))
		expectEqual(t, got.Team, "core")
		other := map[string]any{"name": "ada"}
		expectEqual(t, mustResolve[[]string](t, ctx, got.Names, other), []string{"ada", "core"})
		expectEqual(t, mustResolve[string](t, ctx, got.Later["b"], other), "adab")
		expectEqual(t, mustResolve[string](t, ctx, got.Each[1], other), "ada")
		numbered := mustResolve[map[int]*Expression](t, ctx, mustJaml(t, sb, "\"1\": \"${ (u) => u.name }\"\n"))
		expectEqual(t, mustResolve[string](t, ctx, numbered[1], other), "ada")
	})
}

func TestAnExpressionFieldBindsWhereverTheDocumentHasThePath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "present: ~\nNamed: ${ 'folded' }\n")
		type _Target struct {
			Present *Expression `json:"present"`
			Missing *Expression `json:"missing"`
			Named   *Expression `json:"named"`
		}
		got := mustResolve[_Target](t, t.Context(), document)
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
		document := mustJaml(t, sb, "later: ${ (x) => x + 1 }\nrest:\n  inner: ${ (x) => x * 10 }\n")
		got := mustResolve[_Observed](t, t.Context(), document, 4)
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
		pair := mustExpression(t, sb, "[a, b]")
		values := map[string]any{"a": 1, "b": 2}
		ctx := WithValues(t.Context(), values)
		ctx = WithValues(ctx, map[string]any{"b": 3})
		values["a"] = 9
		expectEqual(t, mustResolve[[]int](t, ctx, pair), []int{1, 3})
		expectEqual(t, mustResolve[[]int](t, WithValues(t.Context(), values), pair), []int{9, 2})
	})
}

func TestEveryLeafSeesTheValuesAsBound(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
replace: ${ (() => { try { user = 1 } catch (e) { return e.name } })() }
remove: ${ (() => { try { delete globalThis.user } catch (e) { return e.name } })() }
deep: ${ (() => { try { user.tags.push('x') } catch (e) { return e.name } })() }
seen: ${ [user.name, user.tags.length, this.user === user] }
`)
		ctx := withValues(t, map[string]any{"user": map[string]any{"name": "ada", "tags": []string{"a"}}})
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
		chain := func(calls int) string {
			return "x => " + strings.Repeat("() => ", calls-1) + "x"
		}
		expectEqual(t, mustResolve[int](t, t.Context(), mustExpression(t, sb, chain(16)), 7), 7)
		_, err := Resolve[int](t.Context(), mustExpression(t, sb, chain(17)), 7)
		expectError(t, err, EvaluationFailed, "$: ", "still a function after 16 calls")
		document := mustJaml(t, sb, "deep:\n  v: \"${ "+chain(17)+" }\"\n")
		_, err = Resolve[any](t.Context(), document)
		expectError(t, err, EvaluationFailed, "$.deep.v: ", "still a function after 16 calls")
	})
}

func TestMergedKeysAreWalkedWhereTheMergeIsWritten(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
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
		ctx := WithScript(t.Context(), "const order = []; function tick(name) { order.push(name); return order.length } var seen = () => order")
		got := mustResolve[map[string]any](t, ctx, document.At("m"))
		expectEqual(t, got["z"], []any{"own a", "base b", "other d", "own c"})
	})
}
