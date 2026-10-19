package jaml

import (
	"encoding/json"
	"errors"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const slowCall = "x => { let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; return x.tag }"

func slowLoops(scale float64) int {
	return int(scale * 4e6 / raceSlowdown)
}

func inFlight(sb *Sandbox) int {
	switch engine := sb.engine.(type) {
	case *isolatedEngine:
		engine.dispatcher.mu.Lock()
		defer engine.dispatcher.mu.Unlock()
		return engine.dispatcher.running
	case *trustedEngine:
		return len(engine.gate)
	}
	return 0
}

func watchPeak(sb *Sandbox) func() int {
	var peak atomic.Int64
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for {
			if running := int64(inFlight(sb)); running > peak.Load() {
				peak.Store(running)
			}
			select {
			case <-done:
				return
			case <-time.After(200 * time.Microsecond):
			}
		}
	}()
	return func() int {
		close(done)
		<-finished
		return int(peak.Load())
	}
}

func callSlowly(t *testing.T, compiled *Expression, jobs int, scale float64) {
	t.Helper()
	var wait sync.WaitGroup
	for job := range jobs {
		wait.Go(func() {
			var tag int
			if err := compiled.Call(within(t, 60*time.Second), &tag, map[string]any{"loops": slowLoops(scale), "tag": job}); err != nil || tag != job {
				t.Errorf("job %d: got %d, %v", job, tag, err)
			}
		})
	}
	wait.Wait()
}

func TestMaxConcurrencyIsHonoured(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		compiled := mustCompile(t, sb, slowCall)
		peak := watchPeak(sb)
		callSlowly(t, compiled, 6, 1)
		expectEqual(t, peak(), 2)
	}, WithMaxConcurrency(2))
}

func TestMaxConcurrencyDefaultsToGOMAXPROCS(t *testing.T) {
	trusted := mustSandbox(t, trustedKind)
	expectEqual(t, cap(trusted.engine.(*trustedEngine).gate), goruntime.GOMAXPROCS(0))
	isolated := mustIsolated(t)
	expectEqual(t, dispatcherOf(t, isolated).limit, goruntime.GOMAXPROCS(0))
}

func TestCallPassesArgumentsAsCopies(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var sum map[string]any
		if err := mustCompile(t, sb, "(a, b) => ({sum: a.n + b.n, keys: Object.keys(a), missing: typeof c})").Call(t.Context(), &sum, map[string]any{"n": 1}, map[string]any{"n": 2}); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, sum, map[string]any{"sum": float64(3), "keys": []any{"n"}, "missing": "undefined"})
		shared := map[string]any{"list": []any{1}}
		var length int
		if err := mustCompile(t, sb, "a => (a.list.push(9), a.list.length)").Call(t.Context(), &length, shared); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, length, 2)
		expectEqual(t, shared, map[string]any{"list": []any{1}})
		var none any
		if err := mustCompile(t, sb, "function () { return arguments.length }").Call(t.Context(), &none); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, none, float64(0))
	})
}

func TestAFunctionNeverLeavesTheSandbox(t *testing.T) {
	cases := map[string]struct {
		source    string
		call      bool
		args      []any
		fragments []string
	}{
		"evaluated function":   {source: "x => x", fragments: []string{"$: x => x: result is a function, which is not YAML data"}},
		"returned function":    {source: "() => () => 1", call: true, fragments: []string{"$: () => () => 1: result is a function, which is not YAML data"}},
		"nested function":      {source: "() => ({f: () => 1})", call: true, fragments: []string{"result.f is a function"}},
		"not a function":       {source: "1 + 1", call: true, fragments: []string{"$: 1 + 1: evaluates to a value of type number, not a function"}},
		"argument not data":    {source: "x => x", call: true, args: []any{func() {}}, fragments: []string{"binding arguments: argument 1 is not JSON data"}},
		"argument too deep":    {source: "x => 1", call: true, args: []any{1, nestedVar(4097)}, fragments: []string{"binding arguments: argument 2 nests deeper than 4096"}},
		"thrown while calling": {source: "() => { throw new TypeError('no') }", call: true, fragments: []string{"$: () => { throw new TypeError('no') }: TypeError: no"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var both bothKinds
			for _, kind := range sandboxKinds {
				compiled := mustCompile(t, mustSandbox(t, kind), c.source)
				var out any
				err := compiled.Eval(t.Context(), &out, nil)
				if c.call {
					err = compiled.Call(t.Context(), &out, c.args...)
				}
				if kind.name == trustedKind.name {
					both.trusted = err
				} else {
					both.isolated = err
				}
			}
			both.expectRefused(t, EvaluationFailed, c.fragments...)
		})
	}
}

func TestAnExpressionFollowsTheTemplateDefaults(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var out any
		expectError(t, mustCompile(t, sb, "x => undefined").Call(t.Context(), &out, 1), CoercionFailed, "$: x => undefined is undefined, which strict undefined coercion refuses")
		loose, err := sb.Compile(t.Context(), "x => undefined", WithUndefinedCoercion(Loose))
		if err != nil {
			t.Fatal(err)
		}
		out = "kept"
		if err := loose.Call(t.Context(), &out, 1); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, out, nil)
		strict, err := sb.Compile(t.Context(), "null", WithNullCoercion(Strict))
		if err != nil {
			t.Fatal(err)
		}
		expectError(t, strict.Eval(t.Context(), &out, nil), CoercionFailed, "strict null")
		_, err = sb.Compile(t.Context(), "1", WithNullCoercion(Coercion(9)))
		expectError(t, err, InvalidTemplate, "null coercion")
		out = "kept"
		if err := mustCompile(t, sb, "null").Eval(t.Context(), &out, nil); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, out, nil)
		_, err = sb.Compile(t.Context(), "1 +")
		expectError(t, err, InvalidTemplate, "1 +: syntax error")
		_, err = sb.Compile(t.Context(), "1; 2")
		expectError(t, err, InvalidTemplate, "write a single JavaScript expression")
		var shaped struct {
			A []int `json:"a"`
		}
		expectError(t, mustCompile(t, sb, "({a: [1, 'x']})").Eval(t.Context(), &shaped, nil), CoercionFailed, "$.a[1]: json: cannot unmarshal string")
	})
}

func TestAFixedClockAndSeedAreDeterministicInBothKinds(t *testing.T) {
	source := "[Date.now(), new Date().toISOString(), Math.random(), Math.random()]"
	at := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	ctx := WithRandomSeed(WithFixedClock(t.Context(), at), 42)
	results := map[string][]any{}
	for _, kind := range sandboxKinds {
		compiled := mustCompile(t, mustSandbox(t, kind), source)
		for range 3 {
			var seen []any
			if err := compiled.Eval(ctx, &seen, nil); err != nil {
				t.Fatal(err)
			}
			if earlier, found := results[kind.name]; found {
				expectEqual(t, seen, earlier)
			}
			results[kind.name] = seen
		}
		var reseeded []any
		if err := compiled.Eval(WithRandomSeed(ctx, 43), &reseeded, nil); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, reseeded[:2], results[kind.name][:2])
		if reseeded[2] == results[kind.name][2] {
			t.Fatalf("%s: seeds 42 and 43 drew the same number", kind.name)
		}
	}
	expectEqual(t, results["isolated"], results["trusted"])
	expectEqual(t, results["trusted"][:2], []any{float64(at.UnixMilli()), "2026-01-02T03:04:05.006Z"})
}

func TestAnUnsetClockStandsStillWithinOneEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		before := time.Now()
		var seen []float64
		if err := mustCompile(t, sb, "(() => { const first = Date.now(); for (let i = 0; i < 1e5; i++); return [first, Date.now()] })()").Eval(t.Context(), &seen, nil); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, seen[0], seen[1])
		if pinned := time.UnixMilli(int64(seen[0])); pinned.Before(before.Truncate(time.Millisecond)) || pinned.After(time.Now()) {
			t.Fatalf("the pinned clock %s is not the submission time", pinned)
		}
	})
}

func TestThrownIsReachableWithItsMessageAndValueInBothKinds(t *testing.T) {
	cases := map[string]struct {
		source  string
		message string
		value   any
	}{
		"object": {"(() => { throw {code: 42, why: 'no', fn() {}, nested: {list: [1, undefined, () => 1]}} })()", "threw an object", map[string]any{"code": float64(42), "why": "no", "nested": map[string]any{"list": []any{float64(1), nil, nil}}}},
		"string": {"(() => { throw 'plain' })()", "threw plain", "plain"},
		"error":  {"(() => { const e = new TypeError('t'); e.status = 404; throw e })()", "TypeError: t", map[string]any{"status": float64(404)}},
		"cycle":  {"(() => { const o = {a: 1}; o.self = o; throw o })()", "threw an object", map[string]any{"a": float64(1), "self": nil}},
		"bigint": {"(() => { throw 10n })()", "threw 10n", nil},
		"nan":    {"(() => { throw [NaN, -0, Infinity] })()", "threw an array", []any{nil, float64(0), nil}},
		"getter": {"(() => { throw {get a() { return 'read' }} })()", "threw an object", map[string]any{"a": "read"}},
		"trap":   {"(() => { throw {ok: 1, get bad() { throw new Error('trap') }} })()", "threw an object", nil},
		"deep":   {"(() => { let a = 'leaf'; for (let i = 0; i < 5000; i++) a = [a]; throw a })()", "threw an array", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var both bothKinds
			for _, kind := range sandboxKinds {
				var out any
				err := mustCompile(t, mustSandbox(t, kind), c.source).Eval(t.Context(), &out, nil)
				var caught *Thrown
				if !errors.As(err, &caught) {
					t.Fatalf("%s: no Thrown in %v", kind.name, err)
				}
				expectEqual(t, caught.Message, c.message)
				if name == "deep" {
					expectDeepCut(t, caught.Value)
				} else {
					expectEqual(t, caught.Value, c.value)
				}
				if kind.name == trustedKind.name {
					both.trusted = err
				} else {
					both.isolated = err
				}
			}
			both.expectRefused(t, EvaluationFailed, c.message)
		})
	}
}

func expectDeepCut(t *testing.T, value any) {
	t.Helper()
	depth := 0
	for {
		list, isList := value.([]any)
		if !isList {
			break
		}
		depth++
		value = list[0]
	}
	expectEqual(t, depth, maxDataDepth)
	expectEqual(t, value, nil)
}

func TestAThrownValueCrossesTheWireAsJSON(t *testing.T) {
	carried := describeFailure(fail(EvaluationFailed, "$.v: %w", &Thrown{Message: "threw an object", Value: map[string]any{"k": []any{"v"}}})).rebuild()
	var caught *Thrown
	if !errors.As(carried, &caught) {
		t.Fatalf("no Thrown in %v", carried)
	}
	expectEqual(t, *caught, Thrown{Message: "threw an object", Value: map[string]any{"k": []any{"v"}}})
	encoded, err := json.Marshal(describeFailure(fail(EvaluationFailed, "plain")))
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, string(encoded), `{"kind":"EvaluationFailed","message":"plain"}`)
}

type keptByValue struct {
	template   Template
	expression Expression
}

func keepByValue(t *testing.T, sb *Sandbox) *keptByValue {
	return &keptByValue{template: *mustTemplate(t, sb, "v: ${ 1 + 1 }\n"), expression: *mustCompile(t, sb, "x => x + 1")}
}

func collectEverythingUnreachable(t *testing.T) {
	t.Helper()
	collected := make(chan struct{})
	decoy := new([64]byte)
	goruntime.AddCleanup(decoy, func(done chan struct{}) { close(done) }, collected)
	decoy = nil
	deadline := time.Now().Add(10 * time.Second)
	for {
		goruntime.GC()
		select {
		case <-collected:
			for range 3 {
				goruntime.GC()
				time.Sleep(10 * time.Millisecond)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the decoy was never collected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestACopiedTemplateOrExpressionOutlivesTheDroppedOriginal(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		kept := keepByValue(t, sb)
		collectEverythingUnreachable(t)
		var two, three int
		if err := kept.template.Eval(t.Context(), &two, nil, "v"); err != nil {
			t.Fatalf("the copied template: %v", err)
		}
		if err := kept.expression.Call(t.Context(), &three, 2); err != nil {
			t.Fatalf("the copied expression: %v", err)
		}
		expectEqual(t, [2]int{two, three}, [2]int{2, 3})
	})
}
