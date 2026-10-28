package jaml

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	goruntime "runtime"
	"strings"
	"testing"
)

func boundValues(t *testing.T, values map[string]any) _BoundValues {
	t.Helper()
	written, err := writeWork(_Ask{values: values}, unboundedAsk)
	if err != nil {
		t.Fatal(err)
	}
	return written.view().Values
}

func askFor(t *testing.T, unit uint64, ask _Ask) _Outgoing {
	t.Helper()
	written, err := writeWork(ask, unboundedAsk)
	if err != nil {
		t.Fatal(err)
	}
	return _Outgoing{unit: unit, body: written.body}
}

func planFor(t *testing.T, plan _Blueprint) _Outgoing {
	t.Helper()
	body, err := writePlan(plan, unboundedAsk)
	if err != nil {
		t.Fatal(err)
	}
	return _Outgoing{body: body}
}

type _WatchedSource struct {
	frame   []byte
	watch   *_HeapWatch
	watched []bool
}

func (s *_WatchedSource) Read(into []byte) (int, error) {
	if len(s.frame) == 0 {
		return 0, io.EOF
	}
	s.watched = append(s.watched, s.watch.working())
	var read = copy(into, s.frame)
	s.frame = s.frame[read:]
	return read, nil
}

func TestTheChildReadsAndDecodesARequestUnderTheWatch(t *testing.T) {
	var outgoing = askFor(t, 7, _Ask{path: []string{"v"}, args: []any{strings.Repeat("x", 1<<20)}})
	var frame = outgoing.body.stamp(9, outgoing.unit)
	var watch = &_HeapWatch{wake: make(chan struct{}, 1)}
	var source = &_WatchedSource{frame: frame, watch: watch}
	var e = &_Evaluator{watch: watch}
	incoming, err := e.receive(_Frames{limit: 4 << 20}.requests(source))
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, incoming.Job, uint64(9))
	expectEqual(t, incoming.Unit, uint64(7))
	expectEqual(t, len(incoming.Work.Args[0].encoded), 1<<20+2)
	expectEqual(t, source.watched[0], false)
	for _, watched := range source.watched[1:] {
		expectEqual(t, watched, true)
	}
	if len(source.watched) < 2 {
		t.Fatalf("the body was never read: %v", source.watched)
	}
	_, err = e.receive(_Frames{limit: 4 << 20}.requests(source))
	if !errors.Is(err, errHungUp) {
		t.Fatalf("a closed request stream: %v", err)
	}
}

func TestAWrittenRequestDecodesToItsWork(t *testing.T) {
	var shape = &_Shape{}
	var ask = _Ask{
		path:    []string{"a", "\u2028"},
		values:  map[string]any{"n": 1.5, "list": []any{nil, true, "s", int64(3), map[string]any{"k": "v"}}, "odd": _CountingMarshaler{calls: new(int)}},
		scripts: []string{"var x = 1"},
		args:    []any{"q", 7},
		skips:   shape,
		pinned:  _Pinned{Millis: -5, Seed: 1 << 63},
	}
	var outgoing = askFor(t, 3, ask)
	var frame = outgoing.body.stamp(1<<40, outgoing.unit)
	var incoming _Request
	if err := json.Unmarshal(frame[frameHeader:], &incoming); err != nil {
		t.Fatalf("%v in %s", err, frame[frameHeader:])
	}
	expectEqual(t, int(binary.BigEndian.Uint64(frame)), len(frame)-frameHeader)
	expectEqual(t, incoming.Op, opServe)
	expectEqual(t, incoming.Job, uint64(1<<40))
	expectEqual(t, incoming.Unit, uint64(3))
	expectEqual(t, incoming.Work.Path, ask.path)
	expectEqual(t, incoming.Work.Scripts, ask.scripts)
	expectEqual(t, incoming.Work.Pinned, ask.pinned)
	expectEqual(t, string(incoming.Work.Values["list"].encoded), `[null,true,"s",3,{"k":"v"}]`)
	expectEqual(t, string(incoming.Work.Values["odd"].encoded), `1`)
	expectEqual(t, string(incoming.Work.Args[1].encoded), `7`)
	for name, value := range ask.values {
		if name == "odd" {
			continue
		}
		want, _ := wireJSON(value)
		expectEqual(t, string(incoming.Work.Values[name].encoded), string(want))
	}
	if incoming.Work.Skips == nil {
		t.Fatal("the skips were lost")
	}
}

func TestAWrittenPlanDecodesToItsBlueprint(t *testing.T) {
	var plan = _Blueprint{Written: "v: \"\\n\"\n", Null: Loose, Undefined: Strict, Raw: true, Function: true, Sigil: "@"}
	var outgoing = planFor(t, plan)
	var incoming _Request
	if err := json.Unmarshal(outgoing.body.stamp(2, 0)[frameHeader:], &incoming); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, incoming.Op, opBuild)
	expectEqual(t, *incoming.Plan, plan)
}

func TestARequestBodyIsStampedAgainForEveryAttempt(t *testing.T) {
	var outgoing = askFor(t, 3, _Ask{args: []any{1}})
	var first = string(outgoing.body.stamp(1, 3))
	var second = outgoing.body.stamp(12345678901, 99)
	var incoming _Request
	if err := json.Unmarshal(second[frameHeader:], &incoming); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, incoming.Job, uint64(12345678901))
	expectEqual(t, incoming.Unit, uint64(99))
	if !strings.HasSuffix(first, string(second[len(second)-20:])) {
		t.Fatal("restamping changed the body")
	}
}

func TestTheScriptCacheEvictsTheLeastRecentlyUsedByWeight(t *testing.T) {
	var first, err = compileScript("var a = 1")
	if err != nil {
		t.Fatal(err)
	}
	var weight = (&_CachedScript{source: "var a = 1", script: first}).weight()
	var cache = newScriptCache(3 * weight)
	for _, source := range []string{"var a = 1", "var b = 1", "var c = 1"} {
		if _, err := cache.compile(source); err != nil {
			t.Fatal(err)
		}
	}
	cache.recall("var a = 1")
	if _, err := cache.compile("var d = 1"); err != nil {
		t.Fatal(err)
	}
	for source, kept := range map[string]bool{"var a = 1": true, "var b = 1": false, "var c = 1": true, "var d = 1": true} {
		expectEqual(t, cache.recall(source) != nil, kept)
	}
	expectEqual(t, cache.held, 3*weight)
	var huge = "var e = '" + strings.Repeat("x", int(weight)) + "'"
	if _, err := cache.compile(huge); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, cache.recall(huge) != nil, false)
	expectEqual(t, cache.held, 3*weight)
}

func TestTheChildScriptBudgetIsAnEighthOfItsLimitUnlessSet(t *testing.T) {
	var budget = func(options ...SandboxOption) (int64, error) {
		configured, err := configureSandbox(options)
		if err != nil {
			return 0, err
		}
		return configured.childScriptBudget()
	}
	var eighth, err = budget(WithMemoryLimit(16 << 20))
	expectEqual(t, eighth, int64(2<<20))
	expectEqual(t, err, nil)
	set, err := budget(WithMemoryLimit(64<<20), WithScriptCacheLimit(48<<20))
	expectEqual(t, set, int64(48<<20))
	expectEqual(t, err, nil)
	_, err = budget(WithMemoryLimit(64<<20), WithScriptCacheLimit(48<<20+1))
	expectError(t, placed(rootPlace, err), InvalidExpression, "script cache limit of 50331649 bytes leaves less than 16777216 bytes of the memory limit of 67108864 bytes")
	_, err = budget(WithScriptCacheLimit(0))
	expectError(t, placed(rootPlace, err), InvalidExpression, "script cache limit must be positive, got 0")
}

func TestAReplyIsReadIntoOneBufferAndDecodedInPlace(t *testing.T) {
	var data = `"` + strings.Repeat("x", 8<<20) + `"`
	var frame = headed([]byte(`{"job":4,"data":` + data + `}`))
	var allocated = uint64(math.MaxUint64)
	for range 3 {
		var stream = &_ReplyStream{frames: _Frames{limit: 16 << 20}.replies(bytes.NewReader(frame), &_Quiet{})}
		var incoming _Reply
		allocated = min(allocated, allocatedIn(func() {
			if err := stream.read(&incoming); err != nil {
				t.Fatal(err)
			}
		}))
		expectEqual(t, string(incoming.Data), data)
	}
	if allocated > uint64(len(frame))+uint64(len(frame))/8 {
		t.Fatalf("reading an 8 MiB reply allocated %d bytes", allocated)
	}
}

func TestAReplyLargerThanTheCapIsRefusedFromItsHeader(t *testing.T) {
	var stream = &_ReplyStream{frames: _Frames{limit: 10}.replies(bytes.NewReader(header(11)), &_Quiet{})}
	var incoming _Reply
	expectEqual(t, fmt.Sprint(stream.read(&incoming)), "the evaluator sent a reply larger than 10 bytes")
}

func allocatedIn(work func()) uint64 {
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	underHeapCeiling(work)
	goruntime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

type _BadlyOutlined struct{ _Servable }

func (_BadlyOutlined) outline() *_CodeOutline {
	return &_CodeOutline{steps: map[string]*_CodeOutline{"a": nil}}
}

func TestAnOutlineThatCannotBeSentSaysTheUnitHoldsCode(t *testing.T) {
	var e = &_Evaluator{replies: &_ReplyWriter{frames: _Frames{limit: 1 << 20}}}
	var incoming _Reply
	if err := json.Unmarshal(e.builtFrame(5, _BadlyOutlined{}), &incoming); err != nil {
		t.Fatal(err)
	}
	if err := incoming.fits(opBuild); err != nil {
		t.Fatalf("a degraded outline does not fit a build: %v", err)
	}
	if incoming.Code == nil || !incoming.Code.holds([]string{"anything"}) {
		t.Fatalf("a degraded outline: %#v", incoming.Code)
	}
}

func TestTheScriptCacheKeepsAFailedCompile(t *testing.T) {
	var cache = newScriptCache(trustedScriptBudget)
	var broken = "const f = " + nested("(a=%s)=>0", "0")(shapeBounds[parameterShape].limit+1)
	_, first := cache.compile(broken)
	if first == nil {
		t.Fatal("the broken script compiled")
	}
	var kept = cache.recall(broken)
	if kept == nil || kept.err != first {
		t.Fatalf("the failure was not kept: %v", kept)
	}
	if _, again := cache.compile(broken); again != first {
		t.Fatalf("compiled again: %v", again)
	}
	expectEqual(t, cache.held, int64(len(broken)+len(first.Error())))
}
