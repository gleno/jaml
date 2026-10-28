package jaml

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
	"time"
)

type _PointerCycle *_PointerCycle
type _PointerCycleA *_PointerCycleB
type _PointerCycleB *_PointerCycleA

func TestPointerOnlyTargetCyclesAreRefused(t *testing.T) {
	const marker = "JAML_TARGET_CYCLE_TEST"
	if os.Getenv(marker) != "1" {
		ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
		defer stop()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		var child = exec.CommandContext(ctx, self, "-test.run=^TestPointerOnlyTargetCyclesAreRefused$", "-test.count=1")
		child.Env = append(os.Environ(), marker+"=1")
		output, err := child.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("target inspection did not terminate: %v\n%s", ctx.Err(), output)
		}
		if err != nil {
			t.Fatalf("target inspection child failed: %v\n%s", err, output)
		}
		return
	}
	debug.SetMaxStack(64 << 20)
	var document = mustExpression(t, mustSandbox(t, trustedKind), "(() => { throw new Error('ran') })()")
	for name, resolve := range map[string]func(context.Context, *Expression) error{
		"self":   resolving[_PointerCycle],
		"mutual": resolving[_PointerCycleA],
		"prefix": resolving[**_PointerCycleA],
		"slice":  resolving[[]_PointerCycle],
		"map":    resolving[map[string]_PointerCycle],
		"field":  resolving[struct{ Loop _PointerCycle }],
	} {
		t.Run(name, func(t *testing.T) {
			expectError(t, resolve(t.Context(), document), InvalidTarget, "recursive pointer type")
		})
	}
}

type _CycleDecoded struct {
	Loop  _PointerCycle
	Value int
}

func (d *_CycleDecoded) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &d.Value)
}

func TestRecursiveTargetsWithConcreteValuesStillDecode(t *testing.T) {
	type _Branch struct {
		Next  *_Branch
		Value int
	}
	var sandbox = mustSandbox(t, trustedKind)
	var branch = mustResolve[_Branch](t, t.Context(), mustJaml(t, sandbox, "Value: 1\nNext: {Value: 2}\n"))
	expectEqual(t, branch.Value, 1)
	expectEqual(t, branch.Next.Value, 2)
	var decoded = mustResolve[_CycleDecoded](t, t.Context(), mustExpression(t, sandbox, "3"))
	expectEqual(t, decoded.Value, 3)
}
