package jaml

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func staticJaml(keys int) string {
	var written strings.Builder
	for index := range keys {
		fmt.Fprintf(&written, "key%d: value %d\n", index, index)
	}
	return written.String()
}

func staticExpression(tb testing.TB, kind _SandboxKind, keys int) *Expression {
	tb.Helper()
	if kind.name == isolatedKind.name && isolationSupported() != nil {
		tb.Skip(isolationSupported())
	}
	sb, err := kind.make(context.Background())
	if err != nil {
		tb.Fatalf("making a sandbox: %v", err)
	}
	made, err := sb.MakeExpressionFromJaml(context.Background(), staticJaml(keys))
	if err != nil {
		tb.Fatalf("MakeExpressionFromJaml: %v", err)
	}
	return made
}

func TestACodeFreeResolveAllocatesIndependentlyOfTheDocumentSize(t *testing.T) {
	var document = staticExpression(t, trustedKind, 10_000)
	if _, err := Resolve[json.RawMessage](t.Context(), document); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var allocations = testing.AllocsPerRun(20, func() {
		if _, err := Resolve[json.RawMessage](t.Context(), document); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	})
	if allocations > 100 {
		t.Fatalf("a code-free Resolve of 10k keys allocated %v times; it should splice the encoded document", allocations)
	}
}

func BenchmarkResolveCodeFreeDocument(b *testing.B) {
	for _, kind := range sandboxKinds {
		b.Run(kind.name, func(b *testing.B) {
			var document = staticExpression(b, kind, 100_000)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Resolve[map[string]any](context.Background(), document); err != nil {
					b.Fatalf("Resolve: %v", err)
				}
			}
		})
	}
}

func BenchmarkResolveCodeFreeLeafInParallel(b *testing.B) {
	var document = staticExpression(b, trustedKind, 4)
	var leaf = document.At("key1")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := Resolve[string](context.Background(), leaf); err != nil {
				b.Fatalf("Resolve: %v", err)
			}
		}
	})
}

var splicingCorpus = []string{
	"plain: text\nnumber: 42\nfloat: 1.5\nbig: 1e21\nsmall: 1e-7\nnegative: -0.0\nhuge: 123456789012345678901234567890\nyes: true\nnothing: null\n",
	"escapes: \"tab\\tquote\\\"slash\\\\ bell\\a nul\\0 line\\u2028 para\\u2029 \\x7f \\U0001F600\"\nunicode: Καλημέρα ☃ 🙂\n\"key \\u2028 with \\\" escapes\": value\n",
	"- 1\n- [a, b, []]\n- {}\n- {x: [1, 2, {y: z}]}\n- \"${{ 'mixed' }} text\"\n",
	"static:\n  deep:\n    deeper: [1, 2, 3]\ncode:\n  leaf: ${ 1 + 1 }\n  beside: {plain: [x, y]}\n  list: [a, \"${ ({o: [null, 1.25, 'é']}) }\", {c: \"${ 'q' }\"}]\n",
	"shared: &shared {a: 1, b: [2, 3]}\ncoded: &coded {c: \"${ 'c' }\", d: [4]}\nagain: *shared\nmore: [*shared, *coded, *shared]\nmerged:\n  <<: [*shared, *coded]\n  own: true\n",
	"absent: ${ null }\nundefined: ${ undefined }\ninterpolated: \"a ${{ null }} b\"\nkept: {x: 1}\n",
	"",
	"just a string",
	"[]",
}

type _SkipsBeside struct {
	Code   *Expression    `json:"code"`
	Static map[string]any `json:"static"`
}

type _SkipsInside struct {
	Static map[string]*Expression `json:"static"`
	Shared *Expression            `json:"shared"`
	More   []*Expression          `json:"more"`
}

func walkedTwin(d *_Document) *_Document {
	var coded = map[any]bool{}
	var mark func(node any) error
	mark = func(node any) error {
		if identity := identityOf(node); identity != nil {
			coded[identity] = true
		}
		return eachChild(node, nil, func(child any, _ *_DataPath) error { return mark(child) })
	}
	_ = mark(d.root)
	return &_Document{root: d.root, compiled: &_Compiled{texts: d.compiled.texts, coded: coded}, settings: d.settings}
}

func renderedBytes(t *testing.T, d *_Document, request _Work) (string, [][]string, error) {
	t.Helper()
	run, err := d.prepare(request)
	if err != nil {
		return "", nil, err
	}
	result, err := run(t.Context())
	if err != nil {
		return "", nil, err
	}
	encoded, err := encodeResult(result)
	return string(encoded), result.skipped, err
}

func TestSplicedResolvesWriteTheSameBytesAsWalkedOnes(t *testing.T) {
	var targets = []reflect.Type{reflect.TypeFor[any](), reflect.TypeFor[_SkipsBeside](), reflect.TypeFor[_SkipsInside]()}
	var coercions = [][]Option{nil, {WithNullCoercion(Strict)}, {WithUndefinedCoercion(Loose)}}
	for index, written := range splicingCorpus {
		for _, options := range coercions {
			configured, err := configure(options)
			if err != nil {
				t.Fatal(err)
			}
			spliced, err := makeDocument(t.Context(), written, configured)
			if err != nil {
				t.Fatalf("document %d: %v", index, err)
			}
			var walked = walkedTwin(spliced)
			for _, target := range targets {
				skips, err := skipsFor(target)
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range [][]string{nil, {"code"}, {"static"}, {"more"}, {"2"}} {
					if _, err := spliced.selectPath(path); err != nil {
						continue
					}
					var request = _Work{Path: path, Skips: skips}
					want, wantSkipped, wantErr := renderedBytes(t, walked, request)
					got, gotSkipped, gotErr := renderedBytes(t, spliced, request)
					if got != want || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !reflect.DeepEqual(gotSkipped, wantSkipped) {
						t.Errorf("document %d at %v into %s with %d options:\nspliced %s %v %v\nwalked  %s %v %v", index, path, target, len(options), got, gotSkipped, gotErr, want, wantSkipped, wantErr)
					}
				}
			}
		}
	}
}

func TestAnExpressionFieldInsideStaticDataStillBindsItsView(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "static: {a: 1, b: [2, 3]}\nshared: &s {x: y}\nmore: [*s, *s]\ncode: ${ 1 }\n")
		var got = mustResolve[_SkipsInside](t, t.Context(), document)
		expectEqual(t, mustResolve[[]int](t, t.Context(), got.Static["b"]), []int{2, 3})
		expectEqual(t, mustResolve[map[string]string](t, t.Context(), got.More[1]), map[string]string{"x": "y"})
		expectEqual(t, mustResolve[map[string]string](t, t.Context(), got.Shared), map[string]string{"x": "y"})
	})
}
