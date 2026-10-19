package jaml

import (
	"errors"
	"strings"
	"testing"
)

func TestAScriptRunsOncePerResolveAndItsGlobalsAreShared(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: ${ bump() }\nb: ${ bump() }\nruns: ${ runs }\n")
		ctx := WithScript(t.Context(), "var runs = 0; runs++; let count = 0; function bump() { return ++count }")
		want := map[string]int{"a": 1, "b": 2, "runs": 1}
		expectEqual(t, mustResolve[map[string]int](t, ctx, document), want)
		expectEqual(t, mustResolve[map[string]int](t, ctx, document), want)
	})
}

func TestScriptsRunInTheOrderAdded(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(t.Context(), "const base = 2")
		ctx = WithScript(ctx, "const { doubled, rest: [first] } = { doubled: base * 2, rest: [base + 1] }")
		expectEqual(t, mustResolve[[]int](t, ctx, mustExpression(t, sb, "[base, doubled, first]")), []int{2, 4, 3})
	})
}

func TestScriptsRunEvenWhenNoLeafDoes(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(t.Context(), "throw new Error('early')")
		_, err := Resolve[map[string]int](ctx, mustJaml(t, sb, "a: 1\n"))
		expectError(t, err, EvaluationFailed, "script 1", "Error: early")
	})
}

func TestValuesShadowScriptDeclarations(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(t.Context(), "const user = 'from the script'; function scripted() { return user }")
		ctx = WithValues(ctx, map[string]any{"user": "from the values"})
		expectEqual(t, mustResolve[[]string](t, ctx, mustExpression(t, sb, "[user, scripted()]")), []string{"from the values", "from the script"})
	})
}

func TestALaterScriptWinsARedeclaredName(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(WithScript(t.Context(), "let name = 'first'"), "let name = 'second'")
		expectEqual(t, mustResolve[string](t, ctx, mustExpression(t, sb, "name")), "second")
	})
}

func TestAThrowingScriptFailsTheResolve(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(WithScript(t.Context(), "const fine = 1"), "throw new RangeError('bad script')")
		_, err := Resolve[int](ctx, mustExpression(t, sb, "fine"))
		expectError(t, err, EvaluationFailed, "script 2: RangeError: bad script")
		var thrown *Thrown
		if !errors.As(err, &thrown) || thrown.Message != "RangeError: bad script" {
			t.Fatalf("expected a Thrown in %v", err)
		}
	})
}

func TestABrokenScriptIsAnInvalidExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		literal := mustExpression(t, sb, "1")
		for source, fragment := range map[string]string{
			"let = ;":                       "script 1: syntax error at line 1",
			"x = 1;\nreturn 2":              "script 1: syntax error at line 2",
			"with (Math) { }":               "script 1: syntax error",
			strings.Repeat(" ", 1<<16+1):    "script 1 is longer than 65536 bytes",
			"let ok = 1; let undefined = 2": "script 1",
		} {
			_, err := Resolve[int](WithScript(t.Context(), source), literal)
			if fragment == "script 1" {
				expectError(t, err, EvaluationFailed, fragment, "undefined")
				continue
			}
			expectError(t, err, InvalidExpression, fragment)
		}
	})
}

func TestScriptGlobalsAreLiveForEveryLeaf(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "set: ${ (total = 40, total) }\nread: ${ total + 2 }\nfixed: ${ (() => { try { pinned = 2 } catch (e) { return e.name } })() }\n")
		ctx := WithScript(t.Context(), "let total = 0; const pinned = 1")
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{"set": float64(40), "read": float64(42), "fixed": "TypeError"})
	})
}

func TestALeafCannotPolluteBuiltInsForALaterLeaf(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
assign: ${ (() => { try { Object.prototype.admin = true; return 'polluted' } catch (e) { return e.name } })() }
define: "${ (() => { try { Object.defineProperty(Array.prototype, 'admin', { value: true }); return 'polluted' } catch (e) { return e.name } })() }"
nested: "${ (() => { try { [][Symbol.iterator]().__proto__.next = () => ({ done: true }); return 'polluted' } catch (e) { return e.name } })() }"
seen: ${ [user.admin === undefined, user.tags.admin === undefined, [...user.tags].length] }
`)
		ctx := withValues(t, map[string]any{"user": map[string]any{"tags": []string{"a", "b"}}})
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{
			"assign": "TypeError",
			"define": "TypeError",
			"nested": "TypeError",
			"seen":   []any{true, true, float64(2)},
		})
	})
}

func TestAScriptMayExtendBuiltInsBeforeTheyFreeze(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx := WithScript(t.Context(), "Array.prototype.total = function () { return this.reduce((sum, n) => sum + n, 0) }")
		document := mustJaml(t, sb, "total: ${ [1, 2, 3].total() }\nreplace: ${ (() => { try { Array.prototype.total = null; return 'replaced' } catch (e) { return e.name } })() }\nagain: ${ [4].total() }\n")
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{"total": float64(6), "replace": "TypeError", "again": float64(4)})
	})
}

func TestAnObjectMayStillOverrideAFrozenBuiltInProperty(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, `
named: "${ Object.assign(new Error(), { name: 'Custom' }).name }"
typed: "${ Object.assign(new TypeError('m'), { name: 'Own', message: 'mine' }).message }"
string: ${ (() => { const o = {}; o.toString = () => 'mine'; return String(o) })() }
pushed: ${ (() => { const list = [1]; list.push = () => 'own'; return list.push(2) })() }
untouched: ${ [String({}), new Error('x').name] }
home: ${ (() => { try { Object.prototype.toString = null; return 'replaced' } catch (e) { return e.name } })() }
`)
		expectEqual(t, mustResolve[map[string]any](t, t.Context(), document), map[string]any{
			"named":     "Custom",
			"typed":     "mine",
			"string":    "mine",
			"pushed":    "own",
			"untouched": []any{"[object Object]", "Error"},
			"home":      "TypeError",
		})
	})
}

func TestNoBuiltInChangeSurvivesIntoTheNextResolve(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		probe := mustExpression(t, sb, "typeof Array.prototype.total")
		extended := WithScript(t.Context(), "Array.prototype.total = function () { return 0 }")
		expectEqual(t, mustResolve[string](t, extended, probe), "function")
		expectEqual(t, mustResolve[string](t, t.Context(), probe), "undefined")
	})
}
