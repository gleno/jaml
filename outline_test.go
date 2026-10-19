package jaml

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

const outlinedJaml = `
static: {a: 1, b: [2, 3]}
shared: &shared {c: "${ 'c' }", d: [4]}
again: *shared
list: [plain, "${{ 'x' }} y", {deep: {deeper: "${ 1 }"}}, [1, 2]]
code: ${ 1 }
`

func outlinedPaths() [][]string {
	return [][]string{nil, {"static"}, {"static", "a"}, {"static", "b", "1"}, {"shared"}, {"shared", "c"}, {"shared", "d"}, {"shared", "d", "0"}, {"again"}, {"again", "c"}, {"again", "d"}, {"list"}, {"list", "0"}, {"list", "1"}, {"list", "2"}, {"list", "2", "deep"}, {"list", "2", "deep", "deeper"}, {"list", "3"}, {"list", "3", "0"}, {"code"}}
}

func TestTheHostOfASecureSandboxKnowsWhereTheCodeIs(t *testing.T) {
	var sb = mustSandbox(t, isolatedKind)
	var remote = mustJaml(t, sb, outlinedJaml).held.keep.unit.(*_RemoteUnit)
	configured, err := configure(nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := makeDocument(t.Context(), outlinedJaml, configured)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range outlinedPaths() {
		selected, err := local.selectPath(path)
		if err != nil {
			t.Fatalf("selecting %v: %v", path, err)
		}
		if got, want := remote.holdsCode(path), local.compiled.holdsCode(selected.node); got != want {
			t.Errorf("the host says %v holds code is %v, the document says %v", path, got, want)
		}
	}
	var raw = mustExpression(t, sb, "1 + 1").held.keep.unit.(*_RemoteUnit)
	expectEqual(t, raw.holdsCode(nil), true)
	var static = mustJaml(t, sb, "a: {b: [1]}").held.keep.unit.(*_RemoteUnit)
	expectEqual(t, static.holdsCode(nil), false)
	expectEqual(t, static.holdsCode([]string{"a", "b"}), false)
}

func TestAnOutlineTooLargeToShipSaysTheWholeDocumentHoldsCode(t *testing.T) {
	var written strings.Builder
	for index := range maxOutlineSteps {
		fmt.Fprintf(&written, "key%d: ${ %d }\n", index, index)
	}
	written.WriteString("static: plain\n")
	configured, err := configure(nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := makeDocument(t.Context(), written.String(), configured)
	if err != nil {
		t.Fatal(err)
	}
	var outline = document.outline()
	expectEqual(t, outline.holds([]string{"static"}), true)
	expectEqual(t, outline.checked(), nil)
}

func TestAnOutlineOfLongKeysStaysWithinItsByteBudget(t *testing.T) {
	var key = strings.Repeat("k", maxOutlineBytes/4)
	var written = fmt.Sprintf("anchor: &k %s\nnest:\n", key)
	for level := 1; level <= 8; level++ {
		written += strings.Repeat("  ", level) + "*k :\n"
	}
	written += strings.Repeat("  ", 9) + "${ 1 }\nstatic: plain\n"
	configured, err := configure(nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := makeDocument(t.Context(), written, configured)
	if err != nil {
		t.Fatal(err)
	}
	var outline = document.outline()
	expectEqual(t, outline.checked(), nil)
	expectEqual(t, outline.holds([]string{"static"}), false)
	expectEqual(t, outline.holds([]string{"nest", key, key, key, key, key, key, key, key}), true)
	encoded, err := wireJSON(outline)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 2*maxOutlineBytes {
		t.Fatalf("the outline encodes to %d bytes", len(encoded))
	}
}

func TestTheHostRefusesABadlyOutlinedReply(t *testing.T) {
	var oversized strings.Builder
	oversized.WriteString(`{"job":1,"built":true,"code":{`)
	for index := range maxOutlineSteps {
		if index > 0 {
			oversized.WriteString(",")
		}
		fmt.Fprintf(&oversized, `"k%d":true`, index)
	}
	oversized.WriteString("}}")
	for _, sent := range []string{
		`{"job":1,"data":1,"code":true}`,
		`{"job":1,"built":true,"code":{"a":null}}`,
		oversized.String(),
	} {
		var reply _Reply
		if err := json.Unmarshal([]byte(sent), &reply); err != nil {
			t.Fatalf("decoding %.60s: %v", sent, err)
		}
		op := opBuild
		if !reply.Built {
			op = opServe
		}
		if reply.fits(op) == nil {
			t.Errorf("the host took %.60s", sent)
		}
	}
	var empty _Reply
	if err := json.Unmarshal([]byte(`{"job":1,"built":true,"code":{}}`), &empty); err == nil {
		t.Errorf("the host decoded an outline step that holds nothing")
	}
}

func TestShippingAnOutlineCostsInProportionToItsDepth(t *testing.T) {
	var shallow, deep = outlineShippingCost(t, 1024), outlineShippingCost(t, 2048)
	if deep > 3*shallow {
		t.Fatalf("shipping an outline twice as deep allocated %d bytes against %d", deep, shallow)
	}
}

func outlineShippingCost(t *testing.T, depth int) uint64 {
	t.Helper()
	configured, err := configure(nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := makeDocument(t.Context(), deepTemplate(depth, `"${ 1 }"`), configured)
	if err != nil {
		t.Fatal(err)
	}
	var outline = document.outline()
	expectEqual(t, outline.checked(), nil)
	var cheapest uint64 = math.MaxUint64
	for range 3 {
		cheapest = min(cheapest, allocatedIn(func() { shipOutline(t, outline) }))
	}
	return cheapest
}

func shipOutline(t *testing.T, outline *_CodeOutline) {
	t.Helper()
	sent, err := _Frames{limit: 1 << 30}.built(1, outline)
	if err != nil {
		t.Fatal(err)
	}
	var received _Reply
	if err := json.Unmarshal(sent, &received); err != nil {
		t.Fatal(err)
	}
	if err := received.fits(opBuild); err != nil {
		t.Fatal(err)
	}
}
