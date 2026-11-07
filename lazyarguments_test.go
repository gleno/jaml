package jaml

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"
)

func denseJSON(t *testing.T, size int) jsontext.Value {
	t.Helper()
	var items []map[string]any
	for i := 0; ; i++ {
		items = append(items, map[string]any{"i": i, "k": fmt.Sprintf("v%d", i), "b": i%2 == 0})
		if i%1000 != 0 {
			continue
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) >= size {
			return encoded
		}
	}
}

func fileSide(t *testing.T) map[string]any {
	var dense = denseJSON(t, 1<<20)
	return map[string]any{"text": string(dense), "data": dense}
}

func boundArgs(t *testing.T, args ...any) []_Datum {
	t.Helper()
	written, err := writeWork(_Ask{args: args}, unboundedAsk)
	if err != nil {
		t.Fatal(err)
	}
	return written.view().Args
}

func limitedCall(t *testing.T) context.Context {
	return WithWallLimit(WithTimeLimit(t.Context(), 25*time.Millisecond*raceSlowdown), 250*time.Millisecond*raceSlowdown)
}

func TestAnUnreadArgumentCostsNothingAgainstTheTimeLimit(t *testing.T) {
	var side = fileSide(t)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var call = func(source string) (any, Usage, error) {
			var used Usage
			result, err := Resolve[any](WithMeter(limitedCall(t), func(spent Usage) { used = spent }), mustExpression(t, sb, source, AsFunction()), side, side, 1)
			return result, used, err
		}
		_, unread, err := call("() => 'x'")
		if err != nil {
			t.Fatalf("an expression that reads no argument failed: %v", err)
		}
		length, _, err := call("(current) => current.text.length")
		if err != nil {
			t.Fatalf("an expression that reads one text failed: %v", err)
		}
		expectEqual(t, length, float64(len(side["text"].(string))))
		_, _, err = call("(current, previous) => JSON.stringify([current.data, previous.data, current.text, previous.text]).length")
		expectError(t, err, TimeLimit)
		t.Logf("unread arguments spent %v of CPU", unread.CPU)
	})
}

func TestAReadArgumentStillPaysTheMemoryLimit(t *testing.T) {
	sb, err := MakeSecureSandbox(t.Context(), WithMemoryLimit(smallestMemoryLimit))
	if err != nil {
		t.Fatal(err)
	}
	var arg = map[string]any{"big": manyObjects(40_000), "small": 1}
	expectEqual(t, mustResolve[float64](t, t.Context(), mustExpression(t, sb, "(x) => x.small + 1", AsFunction()), arg), float64(2))
	var _, read = Resolve[float64](t.Context(), mustExpression(t, sb, "(x) => x.big.length", AsFunction()), arg)
	expectError(t, read, MemoryLimit)
}

func TestASessionPaysOnlyForTheArgumentFieldsItReads(t *testing.T) {
	var args = boundArgs(t, map[string]any{"big": manyObjects(100_000), "small": 1})
	var session = func(source string) time.Duration {
		compiled, err := compileExpression(source, dollar.value)
		if err != nil {
			t.Fatal(err)
		}
		var started = time.Now()
		opened, err := openSession(t.Context(), _SessionPlan{args: args})
		if err != nil {
			t.Fatal(err)
		}
		defer opened.close()
		if _, err := opened.evaluate(compiled, args); err != nil {
			t.Fatal(err)
		}
		return time.Since(started)
	}
	var untouched, small, big = session("() => 1"), session("(x) => x.small + 1"), session("(x) => x.big.length")
	t.Logf("untouched %v, small %v, big %v", untouched, small, big)
	if untouched*10 > big {
		t.Fatalf("a session that never touched its argument spent %v against %v reading a large field", untouched, big)
	}
	if small*3 > big {
		t.Fatalf("a session that read a small field spent %v against %v reading a large one", small, big)
	}
}

func TestAnArgumentFieldIsNotBuiltUntilCodeReadsIt(t *testing.T) {
	var args = []_Datum{{encoded: jsontext.Value(`{"broken": {`)}}
	opened, err := openSession(t.Context(), _SessionPlan{args: args})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	var evaluate = func(source string) error {
		compiled, err := compileExpression(source, dollar.value)
		if err != nil {
			t.Fatal(err)
		}
		_, err = opened.evaluate(compiled, args)
		return err
	}
	if err := evaluate("() => 1"); err != nil {
		t.Fatalf("an argument nobody read failed to bind: %v", err)
	}
	if err := evaluate("(x) => x.broken"); err == nil || !strings.Contains(err.Error(), "argument 1: SyntaxError") {
		t.Fatalf("an argument that cannot be built gave %v", err)
	}
}

func TestAnArgumentReadsLikeThePlainObjectItEncodes(t *testing.T) {
	var probes = []string{
		"Object.keys(o)",
		"Object.getOwnPropertyNames(o)",
		"JSON.stringify(o)",
		"JSON.stringify({ ...o })",
		"Object.entries(o).length",
		"['a' in o, 'zz' in o, o.hasOwnProperty('b'), 'toString' in o]",
		"[typeof o, Array.isArray(o), String(o), Object.prototype.toString.call(o), Object.getPrototypeOf(o) === Object.prototype]",
		"o.b === o.b",
		"(() => { o.a = 9; return [o.a, Object.keys(o)] })()",
		"(() => { delete o.b; return [Object.keys(o), 'b' in o, o.b === undefined] })()",
		"(() => { Object.defineProperty(o, 'a', { enumerable: false }); return [Object.keys(o), o.a] })()",
		"(() => { Object.defineProperty(o, 'g', { get: function () { return this === o }, configurable: true }); return [o.g, Object.keys(o)] })()",
		"Object.getOwnPropertyDescriptor(o, 'b')",
		"Object.getOwnPropertyDescriptor(o, 'missing') === undefined",
		"(() => { Object.freeze(o); let thrown = 'none'; try { o.a = 1 } catch (e) { thrown = e.name } return [Object.isFrozen(o), thrown, o.a] })()",
		"(() => { Object.seal(o); return [Object.isSealed(o), Reflect.deleteProperty(o, 'a'), Reflect.set(o, 'a', 5), o.a] })()",
		"(() => { Object.preventExtensions(o); return [Object.isExtensible(o), Reflect.set(o, 'fresh', 1), Reflect.set(o, 'a', 5), o.a] })()",
		"(() => { const child = Object.create(o); child.a = 3; return [o.a, child.a, Object.keys(child), child.b.c] })()",
		"(() => { const lone = '\\ud800'; o[lone] = 1; const read = o[lone]; delete o[lone]; return [read, o['\\ufffd'], Object.keys(o).length] })()",
		"[Object.keys(o).includes('__proto__'), Object.getPrototypeOf(o) === Object.prototype, o['__proto__'].x]",
		"(() => { for (const key in o) { o[key] = key } return o })()",
		"(() => { const seen = []; for (const key in o) { seen.push(key) } return seen })()",
		"[Object.isFrozen(o), Object.isExtensible(o), Object.isSealed(o)]",
		"(() => { Object.setPrototypeOf(o, null); return [Object.getPrototypeOf(o), 'toString' in o] })()",
	}
	var encoded = `{"a": 1, "b": {"c": [true, null]}, "10": "ten", "2": "two", "�": "real", "__proto__": {"x": 1}}`
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, probe := range probes {
			var source = "(o, json) => { const probe = (o) => { try { return " + probe + " } catch (e) { return String(e) } }; return [probe(o), probe(JSON.parse(json))] }"
			var pair = mustResolve[[]any](t, t.Context(), mustExpression(t, sb, source, AsFunction()), jsontext.Value(encoded), encoded)
			if len(pair) != 2 {
				t.Fatalf("%s gave %v", probe, pair)
			}
			if fmt.Sprint(pair[0]) != fmt.Sprint(pair[1]) {
				t.Fatalf("%s on an argument gave %#v, on a plain object %#v", probe, pair[0], pair[1])
			}
		}
	})
}

func TestAnArgumentIsDataWhenReturnedOrThrown(t *testing.T) {
	var arg = map[string]any{"a": float64(1), "b": map[string]any{"c": []any{true}}}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		expectEqual(t, mustResolve[map[string]any](t, t.Context(), mustExpression(t, sb, "(x) => x", AsFunction()), arg), arg)
		expectEqual(t, mustResolve[[]any](t, t.Context(), mustExpression(t, sb, "(x) => { x.d = 2; return [x, x.b] }", AsFunction()), arg), []any{
			map[string]any{"a": float64(1), "b": map[string]any{"c": []any{true}}, "d": float64(2)},
			map[string]any{"c": []any{true}},
		})
		var _, thrown = Resolve[any](t.Context(), mustExpression(t, sb, "(x) => { throw x }", AsFunction()), arg)
		var _, plain = Resolve[any](t.Context(), mustExpression(t, sb, "(x) => { throw JSON.parse(JSON.stringify(x)) }", AsFunction()), arg)
		expectError(t, thrown, EvaluationFailed)
		var tail = func(err error) string { return err.Error()[strings.Index(err.Error(), "threw"):] }
		expectEqual(t, tail(thrown), tail(plain))
		var _, proxied = Resolve[any](t.Context(), mustExpression(t, sb, "(x) => new Proxy(x, {})", AsFunction()), arg)
		expectError(t, proxied, EvaluationFailed, "result is a proxy")
	})
}

func TestAnArgumentThatIsNotAnObjectIsBuiltAsBefore(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var echo = mustExpression(t, sb, "(...all) => all", AsFunction())
		expectEqual(t, mustResolve[[]any](t, t.Context(), echo, []any{map[string]any{"a": 1}}, "text", 2, nil, true), []any{
			[]any{map[string]any{"a": float64(1)}}, "text", float64(2), nil, true,
		})
	})
}
