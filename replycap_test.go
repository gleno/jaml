//go:build unix && !ios

package jaml

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAnAnswerPastTheReplyCapIsRefusedInTheChild(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(64<<20))
	before := childPID(t, sb)
	var out any
	huge := mustExpression(t, sb, "x => '\\u0001'.repeat(x)")
	expectError(t, resolveInto(within(t, 30*time.Second), &out, huge, 3e6), EvaluationFailed, "$: the answer is larger than 16777216 bytes")
	thrown := mustExpression(t, sb, "x => { throw 'x'.repeat(x) }")
	expectError(t, resolveInto(within(t, 30*time.Second), &out, thrown, 17e6), EvaluationFailed, "$: the answer is larger than 16777216 bytes")
	var text string
	if err := resolveInto(within(t, 30*time.Second), &text, huge, 2e6); err != nil || len(text) != 2e6 {
		t.Fatalf("an answer under the cap: %d bytes, %v", len(text), err)
	}
	expectEqual(t, childPID(t, sb), before)
	var trusted string
	if err := resolveInto(t.Context(), &trusted, mustExpression(t, mustSandbox(t, trustedKind), "x => '\\u0001'.repeat(x)"), 3e6); err != nil || len(trusted) != 3e6 {
		t.Fatalf("a trusted sandbox has no wire and no cap: %d bytes, %v", len(trusted), err)
	}
}

func TestAReplyPastTheCapKillsTheChildAsAProtocolBreak(t *testing.T) {
	sb := mustIsolated(t, WithMemoryLimit(oversizedAnswerLimit))
	document := mustJaml(t, sb, "v: ${ 1 }\n")
	process := currentChild(t, sb)
	expectError(t, evalBounded(t, document, within(t, 30*time.Second), "v"), EvaluationFailed, "the evaluator sent a reply larger than 4194306 bytes")
	expectDeath(t, process)
}

func TestHTMLCharactersCountAsTheyAreAtTheReplyCap(t *testing.T) {
	for _, kind := range sandboxKinds {
		t.Run(kind.name, func(t *testing.T) {
			sb := sharedSandbox(t, kind)
			if kind.name == isolatedKind.name {
				sb = mustIsolated(t, WithMemoryLimit(32<<20))
			}
			for _, text := range []string{"'<'", "'>'", "'&'"} {
				var answer string
				thrown := mustExpression(t, sb, "x => { throw "+text+".repeat(x) }")
				if err := resolveInto(within(t, 30*time.Second), &answer, mustExpression(t, sb, "x => "+text+".repeat(x)"), 2<<20); err != nil || len(answer) != 2<<20 {
					t.Fatalf("%s.repeat(2 MiB) under an 8 MiB cap: %d bytes, %v", text, len(answer), err)
				}
				var caught *Thrown
				if err := resolveInto(within(t, 30*time.Second), &answer, thrown, 2<<20); !errors.As(err, &caught) || caught.Value != strings.Repeat(text[1:2], 2<<20) {
					t.Fatalf("throwing %s.repeat(2 MiB) under an 8 MiB cap: %v", text, err)
				}
			}
		})
	}
}
