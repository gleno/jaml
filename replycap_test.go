//go:build unix && !ios

package jaml

import (
	"testing"
	"time"
)

func TestAnAnswerPastTheReplyCapIsRefusedInTheChild(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20))
	before := childPID(t, sb)
	var out any
	huge := mustCompile(t, sb, "x => '\\u0001'.repeat(x)")
	expectError(t, huge.Call(within(t, 30*time.Second), &out, 3e6), EvaluationFailed, "$: the answer is larger than 16777216 bytes")
	thrown := mustCompile(t, sb, "x => { throw 'x'.repeat(x) }")
	expectError(t, thrown.Call(within(t, 30*time.Second), &out, 17e6), EvaluationFailed, "$: the answer is larger than 16777216 bytes")
	var text string
	if err := huge.Call(within(t, 30*time.Second), &text, 2e6); err != nil || len(text) != 2e6 {
		t.Fatalf("an answer under the cap: %d bytes, %v", len(text), err)
	}
	expectEqual(t, childPID(t, sb), before)
	var trusted string
	if err := mustCompile(t, mustSandbox(t, trustedKind), "x => '\\u0001'.repeat(x)").Call(t.Context(), &trusted, 3e6); err != nil || len(trusted) != 3e6 {
		t.Fatalf("a trusted sandbox has no wire and no cap: %d bytes, %v", len(trusted), err)
	}
}

func TestAReplyPastTheCapKillsTheChildAsAProtocolBreak(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(oversizedAnswerLimit))
	template := mustTemplate(t, sb, "v: ${ 1 }\n")
	process := currentChild(t, sb)
	expectError(t, evalBounded(t, template, within(t, 30*time.Second), "v"), EvaluationFailed, "the evaluator sent a reply larger than 4194306 bytes")
	expectDeath(t, process)
}
