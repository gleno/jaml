//go:build unix && !ios

package jaml

import (
	"testing"
	"time"
)

func TestARunStuckInABuiltInPastItsTimeLimitEndsTheChildAndFailsWithTimeLimit(t *testing.T) {
	var sb = mustIsolated(t)
	var runaway = mustExpression(t, sb, "/^(?=a)(a+)+$/.test('a'.repeat(30) + 'b')")
	var neighbour = mustExpression(t, sb, "(() => { let n = 0; for (let i = 0; i < 3e7; i++) { n += i % 3 } return n > 0 })()")
	var child = currentChild(t, sb)
	var survived = make(chan error, 1)
	go func() {
		var out bool
		survived <- resolveInto(t.Context(), &out, neighbour)
	}()
	var out any
	var err = resolveInto(WithTimeLimit(t.Context(), 50*time.Millisecond), &out, runaway)
	expectError(t, err, TimeLimit, "$: exceeded the time limit of 50ms and was still inside a built-in 250ms after it was told to stop")
	expectStuckAfterCancel(t, child)
	if err := <-survived; err != nil {
		t.Fatalf("a job beside the stuck one failed: %v", err)
	}
}
