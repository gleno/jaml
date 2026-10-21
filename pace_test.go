package jaml

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func manySmallObjects(count int) []any {
	var many = make([]any, count)
	for i := range many {
		many[i] = map[string]any{"a": i}
	}
	return many
}

func carrying(t *testing.T, sb *Sandbox, values map[string]any) time.Duration {
	t.Helper()
	var unread = mustExpression(t, sb, "0")
	var started = time.Now()
	if _, err := Resolve[int](WithValues(t.Context(), values), unread); err != nil {
		t.Fatalf("a call that reads no value failed: %v", err)
	}
	return time.Since(started)
}

func spentBinding(t *testing.T, ctx context.Context, sb *Sandbox, values map[string]any) (time.Duration, error) {
	t.Helper()
	var carried = carrying(t, sb, values)
	var started = time.Now()
	_, err := Resolve[int](WithValues(ctx, values), mustExpression(t, sb, "many.length"))
	return time.Since(started) - carried, err
}

func expectUntouched(t *testing.T, sb *Sandbox, child *_Life) {
	t.Helper()
	if child == nil {
		return
	}
	time.Sleep(4 * cancelGrace)
	if !child.alive() {
		t.Fatalf("the evaluator died: %v", child.lost().cause)
	}
	if current := currentChild(t, sb); current != child {
		t.Fatal("the child was replaced after jaml's own work was stopped")
	}
}

func TestALargeValueStopsAtItsTimeLimitWithoutEndingTheChild(t *testing.T) {
	var values = map[string]any{"many": manySmallObjects(300_000)}
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var child *_Life
		if _, isolated := sb.engine.(*_IsolatedEngine); isolated {
			child = currentChild(t, sb)
		}
		spent, err := spentBinding(t, WithWallLimit(t.Context(), 5*time.Millisecond), sb, values)
		expectError(t, err, WallLimit, "exceeded the wall time limit of 5ms")
		if strings.Contains(err.Error(), "built-in") {
			t.Fatalf("jaml's own work was blamed on a built-in: %v", err)
		}
		t.Logf("stopped %s after the call began binding", spent)
		if spent > raceSlowdown*100*time.Millisecond {
			t.Fatalf("a 5ms limit took %s to stop binding a value", spent)
		}
		expectUntouched(t, sb, child)
	})
}

func TestACancelDuringAValueBindReturnsAtOnce(t *testing.T) {
	var values = map[string]any{"many": manySmallObjects(1_000_000)}
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var child *_Life
		if _, isolated := sb.engine.(*_IsolatedEngine); isolated {
			child = currentChild(t, sb)
		}
		var carried = carrying(t, sb, values)
		var reading = mustExpression(t, sb, "many.length")
		ctx, cancel := context.WithCancel(t.Context())
		var timer = time.AfterFunc(carried+20*time.Millisecond, cancel)
		defer timer.Stop()
		var started = time.Now()
		_, err := Resolve[int](WithValues(ctx, values), reading)
		var spent = time.Since(started) - carried
		expectError(t, err, EvaluationFailed, "context canceled")
		t.Logf("returned %s after the call began binding", spent)
		if spent > raceSlowdown*150*time.Millisecond {
			t.Fatalf("a cancel 20ms into binding returned after %s", spent)
		}
		expectUntouched(t, sb, child)
	})
}

func TestReadingAResultStopsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	opened, err := openSession(ctx, _SessionPlan{})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	built, err := opened.realm.vm.RunString("Array.from({ length: 1000000 }, (_, i) => i)")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	read, err := readResult(opened.realm, built)
	if err == nil {
		t.Fatalf("a result of a million values was read in full after its context ended: %d items", len(read.([]any)))
	}
}

func slowScript() string {
	return "const f = () => [" + strings.TrimSuffix(strings.Repeat(strings.Repeat("a+", 4000)+"a,", 4), ",") + "]"
}

func TestAScriptCompilesOutsideTheTimeLimit(t *testing.T) {
	var script = slowScript()
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var ctx = WithWallLimit(WithScript(WithRandomSeed(WithFixedClock(t.Context(), time.Unix(0, 0)), 1), script), 100*time.Millisecond)
		var kind = mustExpression(t, sb, "typeof f")
		for _, attempt := range []string{"cold", "warm"} {
			got, err := Resolve[string](ctx, kind)
			if err != nil {
				t.Fatalf("a %s script cache changed the outcome: %v", attempt, err)
			}
			expectEqual(t, got, "function")
		}
	})
}

func TestTheScriptCacheLimitIsTheSandboxs(t *testing.T) {
	var leaves = make([]string, 40)
	for i := range leaves {
		leaves[i] = fmt.Sprintf("const leaf%d = '%s'", i, strings.Repeat("x", 60<<10))
	}
	var resident = func(options ...SandboxOption) int {
		var sb = mustSandbox(t, trustedKind, options...)
		var e = mustExpression(t, sb, "1")
		for range 2 {
			for _, leaf := range leaves {
				mustResolve[int](t, WithScript(t.Context(), leaf), e)
			}
		}
		var cache = sb.engine.(*_TrustedEngine).host.scripts
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return len(cache.entries)
	}
	if kept := resident(); kept >= len(leaves) {
		t.Fatalf("the default cache kept all %d leaves; the test no longer shows thrashing", kept)
	}
	expectEqual(t, resident(WithScriptCacheLimit(64<<20)), len(leaves))
	_, err := MakeSecureSandbox(t.Context(), WithMemoryLimit(32<<20), WithScriptCacheLimit(32<<20))
	expectError(t, err, InvalidExpression, "script cache limit of 33554432 bytes leaves less than 16777216 bytes of the memory limit of 33554432 bytes")
}

func TestAScriptCompileIsBoundedByTheBuildTimeout(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var child *_Life
		if _, isolated := sb.engine.(*_IsolatedEngine); isolated {
			child = currentChild(t, sb)
		}
		var ctx = t.Context()
		for index := range 4 {
			ctx = WithScript(ctx, slowScript()+"// "+strconv.Itoa(index))
		}
		_, err := Resolve[string](ctx, mustExpression(t, sb, "typeof f"))
		expectError(t, err, EvaluationFailed, "$: cancelled: the build took longer than its timeout of 50ms")
		if child != nil {
			<-child.ended
			if cause := child.lost().cause; !strings.Contains(cause.Error(), "stuck after cancel") {
				t.Fatalf("the evaluator's death was judged %v, want a job stuck after cancel", cause)
			}
		}
	}, WithBuildTimeout(50*time.Millisecond))
}
