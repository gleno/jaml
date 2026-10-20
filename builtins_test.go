package jaml

import (
	"context"
	"testing"
)

const builtInWrites = `[() => { Array.prototype.admin = true }, () => { Function.prototype.call = () => 'hijacked' }, () => { Math = {} }].map((write) => { try { write(); return 'written' } catch (e) { return e.name } })`

const builtInReads = `[[].admin === true, (function () { return 1 }).call(), typeof Math.max]`

func TestABuiltInChangeReachesALaterLeafOnlyWithoutProtection(t *testing.T) {
	var written = "a: \"${ " + builtInWrites + " }\"\nb: \"${ " + builtInReads + " }\"\n"
	var protected = map[string]any{"a": []any{"TypeError", "TypeError", "TypeError"}, "b": []any{false, float64(1), "function"}}
	var mutable = map[string]any{"a": []any{"written", "written", "written"}, "b": []any{true, "hijacked", "undefined"}}
	var wants = map[string]map[string]any{trustedKind.name: mutable, isolatedKind.name: protected, unprotectedKind.name: mutable}
	for _, kind := range allSandboxKinds {
		t.Run(kind.name, func(t *testing.T) {
			expectEqual(t, mustResolve[map[string]any](t, t.Context(), mustJaml(t, mustSandbox(t, kind), written)), wants[kind.name])
		})
	}
}

func TestNoBuiltInChangeReachesTheNextResolve(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var writer = mustExpression(t, sb, builtInWrites)
		var reader = mustExpression(t, sb, builtInReads)
		for range 3 {
			mustResolve[[]any](t, t.Context(), writer)
			expectEqual(t, mustResolve[[]any](t, t.Context(), reader), []any{false, float64(1), "function"})
		}
	})
}

func TestATrustedResolveDoesNotFreezeTheBuiltIns(t *testing.T) {
	var frozen = testing.AllocsPerRun(10, func() {
		opened, err := openSession(context.Background(), _SessionPlan{})
		if err != nil {
			t.Fatal(err)
		}
		opened.close()
	})
	var expression = mustExpression(t, mustSandbox(t, trustedKind), "[1, 2].map((n) => n + 1).length")
	var resolved = testing.AllocsPerRun(10, func() {
		mustResolve[int](t, t.Context(), expression)
	})
	if resolved*2 > frozen {
		t.Fatalf("a trusted Resolve allocated %v times, against %v for opening a session that freezes the built-ins", resolved, frozen)
	}
}
