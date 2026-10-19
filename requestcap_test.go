//go:build unix && !ios

package jaml

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestARequestPastTheCapIsRefusedInTheHostNamingItsBulk(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(16<<20))
	var before = childPID(t, sb)
	var echo = mustExpression(t, sb, "x => typeof x")
	var huge = strings.Repeat("x", 5<<20)
	var third = strings.Repeat("y", 3<<19)
	var script = "// " + strings.Repeat("z", 64000)
	var scripted = t.Context()
	for index := range 70 {
		scripted = WithScript(scripted, fmt.Sprintf("%s%d", script, index))
	}
	for name, refused := range map[string]struct {
		run  func() error
		bulk string
	}{
		"one value": {func() error {
			var out string
			return resolveInto(valued(t.Context(), map[string]any{"big": huge, "small": 1}), &out, echo, 1)
		}, `value "big"`},
		"values": {func() error {
			var out string
			return resolveInto(valued(t.Context(), map[string]any{"a": third, "b": third, "c": third}), &out, echo, 1)
		}, "the values"},
		"arguments":  {func() error { var out string; return resolveInto(t.Context(), &out, echo, huge) }, "the arguments"},
		"scripts":    {func() error { var out string; return resolveInto(scripted, &out, echo, 1) }, "the scripts"},
		"a document": {func() error { _, err := sb.MakeExpressionFromJaml(t.Context(), "v: "+huge+"\n"); return err }, "the document"},
	} {
		t.Run(name, func(t *testing.T) {
			expectError(t, refused.run(), EvaluationFailed, "the request is larger than 4194304 bytes, mostly "+refused.bulk)
		})
	}
	expectEqual(t, childPID(t, sb), before)
	var plain = mustJaml(t, sb, "v: 1\n")
	var answer int
	if err := resolveInto(valued(t.Context(), map[string]any{"big": strings.Repeat("x", 4<<20-1024)}), &answer, plain.At("v")); err != nil || answer != 1 {
		t.Fatalf("a request under the cap: %d, %v", answer, err)
	}
	var trusted = mustExpression(t, mustSandbox(t, trustedKind), "x => x.length")
	var length int
	if err := resolveInto(t.Context(), &length, trusted, huge); err != nil || length != len(huge) {
		t.Fatalf("a trusted sandbox has no request cap: %d, %v", length, err)
	}
}

func TestTheHostHoldsABigRequestOnce(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20))
	var echo = mustExpression(t, sb, "x => 1")
	var big = strings.Repeat("x", 8<<20)
	var warm int
	if err := resolveInto(t.Context(), &warm, echo, 1); err != nil {
		t.Fatal(err)
	}
	var allocated = allocatedIn(func() {
		var out int
		if err := resolveInto(valued(within(t, 30*time.Second), map[string]any{"big": big}), &out, echo, 1); err != nil {
			t.Fatal(err)
		}
	})
	if allocated > 2*uint64(len(big)) {
		t.Fatalf("sending an 8 MiB value allocated %d MiB in the host, more than twice its size", allocated>>20)
	}
}

func TestTheHostReadsABigAnswerOnce(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(64<<20))
	var huge = mustExpression(t, sb, "x => 'x'.repeat(x)")
	var warm string
	if err := resolveInto(t.Context(), &warm, huge, 1); err != nil {
		t.Fatal(err)
	}
	const size = 12 << 20
	var allocated = allocatedIn(func() {
		var text string
		if err := resolveInto(within(t, 30*time.Second), &text, huge, size); err != nil || len(text) != size {
			t.Fatalf("a 12 MiB answer: %d bytes, %v", len(text), err)
		}
	})
	if allocated > 2*size+size/4 {
		t.Fatalf("a 12 MiB answer decoded into a string allocated %d MiB in the host, more than the frame and the string", allocated>>20)
	}
}

func TestManyDistinctScriptsKeepOneChild(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(16<<20), WithMaxConcurrency(1))
	var sum = mustExpression(t, sb, "1 + 1")
	var before = childPID(t, sb)
	var dense strings.Builder
	for index := 0; dense.Len() < 60<<10; index++ {
		fmt.Fprintf(&dense, "var a%d = %d;\n", index, index)
	}
	for index := range 60 {
		var out int
		var ctx = WithScript(within(t, 30*time.Second), fmt.Sprintf("%s// %d\n", dense.String(), index))
		if err := resolveInto(ctx, &out, sum); err != nil || out != 2 {
			t.Fatalf("resolve %d: %d, %v", index, out, err)
		}
	}
	expectEqual(t, childPID(t, sb), before)
}

func TestABigRequestLeavesNothingBehindInTheChild(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(16<<20), WithMaxConcurrency(1))
	var echo = mustExpression(t, sb, "x => typeof x")
	var hungry = mustExpression(t, sb, hungryExpression)
	var before = childPID(t, sb)
	var big = strings.Repeat("x", 2<<20)
	for range 3 {
		var kind string
		if err := resolveInto(within(t, 30*time.Second), &kind, echo, big); err != nil || kind != "string" {
			t.Fatalf("a 2 MiB request: %q, %v", kind, err)
		}
		var count int
		if err := resolveInto(within(t, 30*time.Second), &count, hungry, hungryCount); err != nil || count != hungryCount {
			t.Fatalf("a job that fits the ceiling alone: %d, %v", count, err)
		}
	}
	expectEqual(t, childPID(t, sb), before)
}

const (
	hungryExpression = "n => 'x'.repeat(n).length"
	hungryCount      = 14 << 20
)

func TestARequestThatDecodesPastTheCeilingIsAMemoryLimit(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(16<<20), WithMaxConcurrency(1))
	var echo = mustExpression(t, sb, "x => 1")
	var many = make(map[string]any, 200_000)
	for index := range 200_000 {
		many[fmt.Sprintf("v%d", index)] = 0
	}
	var out int
	expectError(t, resolveInto(valued(within(t, 60*time.Second), many), &out, echo, 1), EvaluationFailed, "exceeded the memory limit of 16777216 bytes")
}

func TestValuesShipOnlyToCode(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "plain: 1\nnested:\n  quiet: 2\n  loud: ${ v.length }\n")
	var log = recordRequests(t, sb)
	var big = map[string]any{"v": strings.Repeat("x", 1<<20)}
	for _, path := range [][]string{{"plain"}, {"nested", "quiet"}, {"absent"}} {
		var out any
		_ = resolveInto(valued(t.Context(), big), &out, document.At(path...))
		var sent = log.lastServed()
		if sent.size > 4<<10 || len(sent.asked.Work.Values) != 0 {
			t.Fatalf("a code-free selection %v sent %d bytes and %d values", path, sent.size, len(sent.asked.Work.Values))
		}
	}
	expectEqual(t, isolatedEvalAs[int](t, document, big, "plain"), 1)
	for _, path := range [][]string{{"nested", "loud"}, {"nested"}, nil} {
		var out any
		if err := resolveInto(valued(t.Context(), big), &out, document.At(path...)); err != nil {
			t.Fatalf("a selection with code %v: %v", path, err)
		}
		if sent := log.lastServed(); sent.size < 1<<20 {
			t.Fatalf("a selection with code %v sent only %d bytes", path, sent.size)
		}
	}
	expectEqual(t, isolatedEvalAs[int](t, document, big, "nested", "loud"), 1<<20)
	var scripted int
	if err := resolveInto(WithScript(valued(t.Context(), big), "if (v.length !== 1048576) throw new Error('no v')"), &scripted, document.At("plain")); err != nil || scripted != 1 {
		t.Fatalf("a script on a code-free selection: %d, %v", scripted, err)
	}
	if sent := log.lastServed(); sent.size < 1<<20 {
		t.Fatalf("a script on a code-free selection sent only %d bytes", sent.size)
	}
	var out any
	expectError(t, resolveInto(valued(t.Context(), map[string]any{"f": func() {}}), &out, document.At("plain")), EvaluationFailed, `binding values: value "f" is not JSON data`)
	expectError(t, resolveInto(valued(t.Context(), map[string]any{"Math": 1}), &out, document.At("plain")), EvaluationFailed, `value "Math" collides with a JavaScript global`)
}
