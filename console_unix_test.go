//go:build unix && !ios

package jaml

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

const threeThenBomb = "x => { console.log('one'); console.info('two', 2); console.warn({a: 1}); return 'x'.repeat(1e9).length }"

func TestLinesWrittenBeforeAMemoryKillSurviveIt(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20))
	var bomb = mustExpression(t, sb, threeThenBomb)
	var log = recordRequests(t, sb)
	ctx, heard := listening(within(t, 60*time.Second))
	var out any
	expectError(t, resolveInto(ctx, &out, bomb), MemoryLimit)
	expectEqual(t, heard.all(), threeLines)
	expectEqual(t, len(log.served(unitOf(bomb))), 2)
}

func TestASiblingReplayedAfterAMemoryKillDeliversEachLineOnce(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20), WithMaxConcurrency(4))
	var sibling = mustExpression(t, sb, "x => { console.log('start', x.tag); let n = 0; for (let i = 0; i < x.loops; i++) n += i % 3; console.log('end', x.tag); return x.tag }")
	var bomb = mustExpression(t, sb, threeThenBomb)
	var log = recordRequests(t, sb)
	var before = childPID(t, sb)
	var group sync.WaitGroup
	for tag := range 3 {
		group.Go(func() {
			ctx, heard := listening(within(t, 60*time.Second))
			if got, err := Resolve[int](ctx, sibling, map[string]any{"loops": slowLoops(1), "tag": tag}); err != nil || got != tag {
				t.Errorf("sibling %d: got %d, %v", tag, got, err)
				return
			}
			if got, want := heard.texts(), []string{fmt.Sprint("start ", tag), fmt.Sprint("end ", tag)}; !equalTexts(got, want) {
				t.Errorf("sibling %d heard %q, want %q", tag, got, want)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	ctx, heard := listening(within(t, 60*time.Second))
	var out any
	expectError(t, resolveInto(ctx, &out, bomb), MemoryLimit)
	group.Wait()
	expectEqual(t, heard.all(), threeLines)
	if childPID(t, sb) == before {
		t.Fatal("the bomb did not take its evaluator down")
	}
	if served := len(log.served(unitOf(sibling))); served != 6 {
		t.Fatalf("the siblings were served %d times, want 6: each once beside the bomb and once replayed", served)
	}
	expectEqual(t, len(log.served(unitOf(bomb))), 2)
}
