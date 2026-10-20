package jaml

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type _Heard struct {
	mu      sync.Mutex
	lines   []Line
	inside  atomic.Int32
	overlap atomic.Bool
}

func (h *_Heard) sink(line Line) {
	if h.inside.Add(1) > 1 {
		h.overlap.Store(true)
	}
	defer h.inside.Add(-1)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line)
}

func (h *_Heard) all() []Line {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Line(nil), h.lines...)
}

func (h *_Heard) texts() []string {
	var texts []string
	for _, line := range h.all() {
		texts = append(texts, line.Text)
	}
	return texts
}

func listening(ctx context.Context) (context.Context, *_Heard) {
	var heard = &_Heard{}
	return WithConsole(ctx, heard.sink), heard
}

const threeThenSpin = "x => { console.log('one'); console.info('two', 2); console.warn({a: 1}); while (true) {} }"

var threeLines = []Line{{Level: Log, Text: "one"}, {Level: Info, Text: "two 2"}, {Level: Warn, Text: `{"a":1}`}}

func TestLinesWrittenBeforeATimeLimitSurviveIt(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "spin: \"${ "+threeThenSpin+" }\"\n")
		ctx, heard := listening(WithWallLimit(t.Context(), 200*time.Millisecond))
		var out any
		expectError(t, resolveInto(ctx, &out, document.At("spin")), WallLimit)
		expectEqual(t, heard.all(), placedAt([]string{"spin"}, threeLines))
	})
}

func TestLinesWrittenBeforeACancelSurviveIt(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var spinner = mustExpression(t, sb, threeThenSpin)
		ctx, cancel := context.WithCancel(t.Context())
		ctx, heard := listening(ctx)
		time.AfterFunc(300*time.Millisecond, cancel)
		var out any
		expectError(t, resolveInto(ctx, &out, spinner), context.Canceled)
		expectEqual(t, heard.all(), threeLines)
	})
}

func TestNoConsoleWithoutTheOption(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var probe = mustExpression(t, sb, "typeof console")
		expectEqual(t, mustResolve[string](t, t.Context(), probe), "undefined")
		ctx, _ := listening(t.Context())
		expectEqual(t, mustResolve[string](t, ctx, probe), "object")
		expectEqual(t, mustResolve[string](t, t.Context(), probe), "undefined")
		var out any
		expectError(t, resolveInto(t.Context(), &out, mustExpression(t, sb, "console.log('x')")), EvaluationFailed, "console is not defined")
	})
}

func TestTheConsoleCannotBeReplaced(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		ctx, heard := listening(t.Context())
		var tamper = mustJaml(t, sb, `
first: ${ (() => { try { console.log = null } catch (e) {} try { console = null } catch (e) {} return typeof console.log })() }
second: ${ (console.log('still'), typeof console.log) }
`)
		expectEqual(t, mustResolve[map[string]string](t, ctx, tamper), map[string]string{"first": "function", "second": "function"})
		expectEqual(t, heard.texts(), []string{"still"})
		var out any
		expectError(t, resolveInto(WithValues(ctx, map[string]any{"console": 1}), &out, mustExpression(t, sb, "1")), EvaluationFailed, `value "console" cannot be bound`)
	})
}

func TestTheConsoleFormatsLikeThePlayground(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		ctx, heard := listening(t.Context())
		var said = mustExpression(t, sb, `(() => {
			const cyclic = {}; cyclic.self = cyclic
			console.log('s', 1, 1.5, -0, true, null, undefined, NaN, {a: [1, 'x']}, [1, 2], function f() {}, cyclic, new String('boxed'), 10n)
			console.error()
			console.debug('d')
			return 0
		})()`)
		mustResolve[int](t, ctx, said)
		expectEqual(t, heard.all(), []Line{
			{Level: Log, Text: `s 1 1.5 0 true null undefined null {"a":[1,"x"]} [1,2] function f() {} [object Object] "boxed" 10`},
			{Level: Error, Text: ""},
			{Level: Debug, Text: "d"},
		})
	})
}

func TestLinesNameThePieceOfCodeThatWroteThem(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
a: ${ console.log('in a') || 1 }
b:
  c: "x ${{ console.log('in c') || 'y' }}"
  d: plain
list: ["${ console.log('in 0') || 0 }"]
`)
		ctx, heard := listening(WithScript(t.Context(), "console.log('in script')"))
		mustResolve[any](t, ctx, document)
		expectEqual(t, heard.all(), []Line{
			{Level: Log, Text: "in script"},
			{Path: []string{"a"}, Level: Log, Text: "in a"},
			{Path: []string{"b", "c"}, Level: Log, Text: "in c"},
			{Path: []string{"list", "0"}, Level: Log, Text: "in 0"},
		})
		ctx, heard = listening(WithScript(t.Context(), "console.log('in script')"))
		mustResolve[any](t, ctx, document.At("b"))
		expectEqual(t, heard.all(), []Line{
			{Path: []string{"b"}, Level: Log, Text: "in script"},
			{Path: []string{"b", "c"}, Level: Log, Text: "in c"},
		})
	})
}

func TestEachResolveHearsItsOwnLinesInOrder(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var talker = mustExpression(t, sb, "x => { for (let i = 0; i < 200; i++) console.log(x.tag, i); return x.tag }")
		var group sync.WaitGroup
		for tag := range 8 {
			group.Go(func() {
				ctx, heard := listening(t.Context())
				if got, err := Resolve[int](ctx, talker, map[string]any{"tag": tag}); err != nil || got != tag {
					t.Errorf("talker %d: got %d, %v", tag, got, err)
					return
				}
				var want []string
				for i := range 200 {
					want = append(want, fmt.Sprintf("%d %d", tag, i))
				}
				if got := heard.texts(); !equalTexts(got, want) {
					t.Errorf("talker %d heard %d lines out of order or mixed: %v", tag, len(got), got[:min(len(got), 5)])
				}
				if heard.overlap.Load() {
					t.Errorf("talker %d: the sink ran on two goroutines at once", tag)
				}
			})
		}
		group.Wait()
	}, WithMaxConcurrency(4))
}

func TestASlowSinkStallsNoOtherResolve(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var talker = mustExpression(t, sb, "x => { for (let i = 0; i < 20; i++) console.log(i); return x }")
		var release = make(chan struct{})
		var stalled = make(chan struct{})
		var once sync.Once
		var slow = WithConsole(t.Context(), func(Line) {
			once.Do(func() { close(stalled) })
			<-release
		})
		var done = make(chan error, 1)
		go func() {
			_, err := Resolve[int](slow, talker, 1)
			done <- err
		}()
		<-stalled
		ctx, heard := listening(within(t, 10*time.Second))
		expectEqual(t, mustResolve[int](t, ctx, talker, 2), 2)
		expectEqual(t, len(heard.all()), 20)
		select {
		case err := <-done:
			t.Fatalf("Resolve returned before its sink heard every line: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}, WithMaxConcurrency(4))
}

func TestLinesPastTheCapsAreDroppedWithOneWarning(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		ctx, heard := listening(t.Context())
		mustResolve[int](t, ctx, mustExpression(t, sb, "(() => { for (let i = 0; i < 1500; i++) console.log(i); return 0 })()"))
		var lines = heard.all()
		expectEqual(t, len(lines), consoleLineCap+1)
		expectEqual(t, lines[consoleLineCap-1].Text, "999")
		expectEqual(t, lines[consoleLineCap], Line{Level: Warn, Text: "jaml: dropped 500 console lines past the limit of 1000 lines or 1048576 bytes per Resolve"})

		ctx, heard = listening(t.Context())
		mustResolve[int](t, ctx, mustExpression(t, sb, "(() => { for (let i = 0; i < 200; i++) console.log('x'.repeat(8000)); return 0 })()"))
		lines = heard.all()
		expectEqual(t, len(lines), consoleByteCap/8000+1)
		expectEqual(t, lines[len(lines)-1].Text, fmt.Sprintf("jaml: dropped %d console lines past the limit of 1000 lines or 1048576 bytes per Resolve", 200-consoleByteCap/8000))

		ctx, heard = listening(t.Context())
		mustResolve[int](t, ctx, mustExpression(t, sb, "(() => { console.log('é'.repeat(9000)); console.log('after'); return 0 })()"))
		lines = heard.all()
		expectEqual(t, len(lines), 2)
		if long := lines[0].Text; len(long) > consoleLineSize || !strings.HasSuffix(long, consoleEllipsis) || !strings.HasPrefix(long, "éé") || strings.ContainsRune(long, utf8.RuneError) {
			t.Fatalf("an overlong line was kept as %d bytes ending %q", len(long), long[max(0, len(long)-8):])
		}
		expectEqual(t, lines[1].Text, "after")
	})
}

func TestConsoleLinesOfAFailingResolveAreStillDelivered(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		ctx, heard := listening(t.Context())
		var out any
		var err = resolveInto(ctx, &out, mustExpression(t, sb, "(() => { console.log('before'); throw new Error('boom') })()"))
		expectError(t, err, EvaluationFailed, "boom")
		expectEqual(t, heard.texts(), []string{"before"})
	})
}

func equalTexts(got, want []string) bool {
	return len(got) == len(want) && strings.Join(got, "\n") == strings.Join(want, "\n")
}

func placedAt(path []string, lines []Line) []Line {
	var placed = make([]Line, len(lines))
	for index, line := range lines {
		line.Path = path
		placed[index] = line
	}
	return placed
}

func BenchmarkResolveACodeLeaf(b *testing.B) {
	for _, kind := range sandboxKinds {
		b.Run(kind.name, func(b *testing.B) {
			sb, err := kind.make(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			leaf, err := sb.MakeExpression(b.Context(), "x => x + 1")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if got, err := Resolve[int](context.Background(), leaf, 1); err != nil || got != 2 {
					b.Fatal(got, err)
				}
			}
		})
	}
}

func TestTheSwitchboardRefusesMalformedLinesAndIgnoresStrayOnes(t *testing.T) {
	var board = &_Switchboard{pending: map[uint64]*_Expectation{}}
	ctx, heard := listening(t.Context())
	var console = consoleFor(ctx, nil)
	board.expect(1, opServe, console)
	board.expect(2, opServe, nil)
	var said = func(job uint64, line _LineOnWire) _Reply { return _Reply{Job: job, Line: &line} }
	if err := board.route(_Reply{Job: 1, Missing: true, Line: &_LineOnWire{Text: "x"}}); err == nil {
		t.Fatal("a line carrying an answer was accepted")
	}
	if err := board.route(said(1, _LineOnWire{Level: Debug + 1, Text: "x"})); err == nil {
		t.Fatal("a line at an unknown level was accepted")
	}
	for _, stray := range []_Reply{said(2, _LineOnWire{Text: "no console"}), said(9, _LineOnWire{Text: "no job"})} {
		if err := board.route(stray); err != nil {
			t.Fatalf("a stray line killed the evaluator: %v", err)
		}
	}
	if err := board.route(said(1, _LineOnWire{Path: []string{"a"}, Level: Warn, Text: "kept"})); err != nil {
		t.Fatal(err)
	}
	board.hangUp()
	console.finish()
	expectEqual(t, heard.all(), []Line{{Path: []string{"a"}, Level: Warn, Text: "kept"}})
}
