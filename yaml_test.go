package jaml

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestOnlyTrueAndFalseAreBools(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "y: y\nyes: yes\non: on\nn: n\ntruth: true\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"y": "y", "yes": "yes", "on": "on", "n": "n", "truth": true})
	})
}

func TestNonStringKeysAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var cases = map[string]string{
			"integer": "1: x\n",
			"bool":    "outer:\n  true: x\n",
			"null":    "~: x\n",
		}
		for name, text := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := sb.MakeExpressionFromJaml(t.Context(), text)
				expectError(t, err, InvalidExpression, "keys must be strings")
			})
		}
		_, err := sb.MakeExpressionFromJaml(t.Context(), "outer:\n  true: x\n")
		expectError(t, err, InvalidExpression, "$.outer: key true is a bool")
	})
}

func TestQuotedKeysAreStrings(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = evalAs[map[string]string](t, mustJaml(t, sb, "\"1\": x\n\"true\": y\n"), nil)
		expectEqual(t, got, map[string]string{"1": "x", "true": "y"})
	})
}

func TestInfinityAndNaNAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: .inf\n", "a: -.inf\n", "a: [.nan]\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "not a finite number")
		}
	})
}

func TestDuplicateKeysAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\na: 2\n")
		expectError(t, err, InvalidExpression, `mapping key "a" already defined at line 1`)
	})
}

func TestDatesKeepTheirSpelling(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type _Decoded struct {
			At     time.Time `json:"at"`
			Plain  string    `json:"plain"`
			Quoted string    `json:"quoted"`
			Tagged string    `json:"tagged"`
		}
		var parsed = mustJaml(t, sb, "at: 2026-10-18T10:00:00Z\nplain: 2026-10-18\nquoted: \"2026-10-18\"\ntagged: !!timestamp 2026-10-18 10:00:00\n")
		var got = mustResolve[_Decoded](t, t.Context(), parsed)
		expectEqual(t, got, _Decoded{
			At:     time.Date(2026, time.October, 18, 10, 0, 0, 0, time.UTC),
			Plain:  "2026-10-18",
			Quoted: "2026-10-18",
			Tagged: "2026-10-18 10:00:00",
		})
		var dates = evalAs[map[string]any](t, mustJaml(t, sb, "base: &d 2026-10-18\ncopy: *d\nlist: [2026-01-02]\n"), nil)
		expectEqual(t, dates, map[string]any{"base": "2026-10-18", "copy": "2026-10-18", "list": []any{"2026-01-02"}})
	})
}

func TestMergeKeysMerge(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "base: &b {x: 1}\nm:\n  <<: *b\n  y: \"${ 1 + 1 }\"\n")
		expectEqual(t, evalAs[map[string]int](t, parsed, nil, "m"), map[string]int{"x": 1, "y": 2})
	})
}

func TestASecondDocumentIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n---\nb: 2\n")
		expectError(t, err, InvalidExpression, "line 2 starts a second YAML document")
		var got = evalAs[map[string]int](t, mustJaml(t, sb, "---\na: 1\n---\n"), nil)
		expectEqual(t, got, map[string]int{"a": 1})
	})
}

func TestIntegersBeyondInt64AreText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
max: 9223372036854775807
over: 9223372036854775808
under: -9223372036854775809
huge: 123456789012345678901234567890
hex: 0x8000000000000000
grouped: 1_000
18446744073709551616: key
`)
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{
			"max":                  float64(9223372036854775807),
			"over":                 "9223372036854775808",
			"under":                "-9223372036854775809",
			"huge":                 "123456789012345678901234567890",
			"hex":                  "0x8000000000000000",
			"grouped":              float64(1000),
			"18446744073709551616": "key",
		})
		expectEqual(t, mustResolve[int64](t, t.Context(), parsed.At("max")), int64(9223372036854775807))
		var huge big.Int
		if err := huge.UnmarshalText([]byte(mustResolve[string](t, t.Context(), parsed.At("huge")))); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, huge.String(), "123456789012345678901234567890")
	})
}

func TestLegacyOctalStaysText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "legacy: 0644\nnegative: -0755\nexplicit: 0o644\nzero: 0\ntagged: !!int 0644\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"legacy": "0644", "negative": "-0755", "explicit": float64(420), "zero": float64(0), "tagged": float64(420)})
		var mode int
		expectError(t, resolveInto(t.Context(), &mode, parsed.At("legacy")), CoercionFailed, "$.legacy", "cannot unmarshal string")
	})
}

func TestOutOfRangeFloatsAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"v: 0.1e400\n", "v: [-1e999]\n", "v: {deep: 1_0e999}\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "out of the range of a 64-bit float")
		}
		_, err := sb.MakeExpressionFromJaml(t.Context(), "outer:\n  v: 1e400\n")
		expectError(t, err, InvalidExpression, "$.outer.v: 1e400")
		var got = evalAs[map[string]any](t, mustJaml(t, sb, "quoted: \"1e400\"\ntiny: 1e-400\nbig: 1e308\n"), nil)
		expectEqual(t, got, map[string]any{"quoted": "1e400", "tiny": float64(0), "big": 1e308})
	})
}

func TestIntegersUpToInt64KeepTheirDigits(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		expectEqual(t, mustResolve[int64](t, t.Context(), mustJaml(t, sb, "v: 9007199254740993\n").At("v")), int64(9007199254740993))
	})
}

func TestAnEmptyDocumentIsNull(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		expectEqual(t, mustResolve[*int](t, t.Context(), mustJaml(t, sb, "")), (*int)(nil))
		expectEqual(t, mustResolve[any](t, t.Context(), mustJaml(t, sb, "")), nil)
	})
}

func TestInvalidYAMLFailsAtMakeTemplate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: [1, 2\n")
		expectError(t, err, InvalidExpression)
	})
}

func TestAliasesExpandWithTheirExpressions(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "base: &b {count: \"${ count }\"}\ncopy: *b\n")
		var got = evalAs[map[string]map[string]int](t, parsed, map[string]any{"count": 4})
		expectEqual(t, got, map[string]map[string]int{"base": {"count": 4}, "copy": {"count": 4}})
	})
}

func TestCommentsDoNotLeakIntoValues(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: ${1+1} # two\nb: \"# not a comment\"\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"a": float64(2), "b": "# not a comment"})
	})
}

func TestUnderscoredLegacyOctalStaysText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: 0_644\nb: -0_644\nc: 0_89\nd: +0_7\ngrouped: 1_000\nhex: 0x_ff\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"a": "0_644", "b": "-0_644", "c": "0_89", "d": "+0_7", "grouped": float64(1000), "hex": float64(255)})
	})
}

func TestBinaryBecomesItsDecodedText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = evalAs[map[string]any](t, mustJaml(t, sb, "v: !!binary aGk=\n!!binary YQ==: key\n"), nil)
		expectEqual(t, got, map[string]any{"v": "hi", "a": "key"})
	})
}

func TestBinaryThatIsNotUTF8IsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "outer:\n  v: !!binary /w==\n")
		expectError(t, err, InvalidExpression, "$.outer.v", "UTF-8")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "!!binary /w==: 1\n")
		expectError(t, err, InvalidExpression, "$", "UTF-8")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "v: !!binary '!!'\n")
		expectError(t, err, InvalidExpression, "$.v", "base64")
	})
}

func TestBinaryKeysAreCheckedForDuplicates(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: 1\n!!binary YQ==: 2\n")
		expectError(t, err, InvalidExpression, `mapping key "a" already defined at line 1`)
	})
}

func TestErrorsUnderMergeKeysNameTheDataPath(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "x:\n  <<: {a: 1e400}\n")
		expectError(t, err, InvalidExpression, "$.x.a: 1e400")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "x:\n  <<: {a: !!binary /w==}\n")
		expectError(t, err, InvalidExpression, "$.x.a")
		if strings.Contains(err.Error(), "<<") {
			t.Fatalf("path runs through the merge key: %v", err)
		}
	})
}

func TestMergeKeysFollowTheMergeSpec(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
a: &a {k: 1, p: a, deep: {x: 1}}
b: &b {k: 2, q: b, r: b}
m:
  r: own
  <<: [*a, *b]
inline:
  <<: {z: 1}
  z: 2
`)
		expectEqual(t, evalAs[map[string]any](t, parsed, nil, "m"), map[string]any{"k": float64(1), "p": "a", "q": "b", "r": "own", "deep": map[string]any{"x": float64(1)}})
		expectEqual(t, evalAs[map[string]any](t, parsed, nil, "inline"), map[string]any{"z": float64(2)})
		for _, text := range []string{"m:\n  <<: 1\n", "a: &a [1]\nm:\n  <<: *a\n", "m:\n  <<: [1]\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "$.m", "merge")
		}
		_, err := sb.MakeExpressionFromJaml(t.Context(), "m:\n  <<: {a: 1}\n  <<: {b: 1}\n")
		expectError(t, err, InvalidExpression, "$.m: line 3: a mapping takes one merge key (<<) and line 2 already has it")
	})
}

func TestQuotedMergeKeyIsAPlainKey(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = evalAs[map[string]any](t, mustJaml(t, sb, "\"<<\": {a: 1}\n"), nil)
		expectEqual(t, got, map[string]any{"<<": map[string]any{"a": float64(1)}})
	})
}

func TestAliasBombsAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var text strings.Builder
		text.WriteString("l0: &l0 [lol, lol, lol, lol, lol, lol, lol, lol, lol, lol]\n")
		for level := 1; level <= 9; level++ {
			fmt.Fprintf(&text, "l%d: &l%d [", level, level)
			for i := range 10 {
				if i > 0 {
					text.WriteString(", ")
				}
				fmt.Fprintf(&text, "*l%d", level-1)
			}
			text.WriteString("]\n")
		}
		_, err := sb.MakeExpressionFromJaml(t.Context(), text.String())
		expectError(t, err, InvalidExpression, "$.l3[", "excessive aliasing")
	})
}

func TestAWideAliasFanOutIsExcessiveAliasing(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var item = "{k0: v,k1: v,k2: v,k3: v,k4: v,k5: v,k6: v,k7: v,k8: v,k9: v}"
		var text = "a: &a [" + strings.TrimSuffix(strings.Repeat(item+",", 1000), ",") + "]\nb: [" + strings.TrimSuffix(strings.Repeat("*a,", 90), ",") + "]\n"
		_, err := sb.MakeExpressionFromJaml(t.Context(), text)
		expectError(t, err, InvalidExpression, "$.b[", "excessive aliasing")
	}, WithBuildTimeout(30*time.Second))
}

func TestModestAliasingIsAccepted(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var text strings.Builder
		text.WriteString("defaults: &defaults {")
		for key := range 20 {
			fmt.Fprintf(&text, "d%d: %d, ", key, key)
		}
		text.WriteString("}\nlabel: &label release\nservices:\n")
		for service := range 300 {
			fmt.Fprintf(&text, "  s%d: {<<: *defaults, name: s%d, tag: *label, port: %d}\n", service, service, 8000+service)
		}
		var got = evalAs[map[string]map[string]any](t, mustJaml(t, sb, text.String()), nil, "services")
		expectEqual(t, len(got), 300)
		expectEqual(t, got["s299"]["d19"], float64(19))
		expectEqual(t, got["s299"]["tag"], "release")
	})
}

func TestScalarTagsResolveByJamlRules(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "s: !!str 12\ni: !!int 0644\nf: !!float 3\nb: !!bool true\nn: !!null ~\ncustom: !thing 12\nquoted: '12'\nnul: null\ntilde: ~\nempty:\ncapital: True\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"s": "12", "i": float64(420), "f": float64(3), "b": true, "n": nil, "custom": "12", "quoted": "12", "nul": nil, "tilde": nil, "empty": nil, "capital": true})
		for _, text := range []string{"v: !!int x\n", "v: !!int 089\n", "v: !!float x\n", "v: !!bool yes\n", "v: !!null x\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "$.v")
		}
	})
}

func TestEmptyTaggedValuesAreNamedInErrors(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for tag, fragment := range map[string]string{"!!bool": `!!bool "" is not one of its spellings`, "!!int": `!!int "" is not an integer`, "!!float": `!!float "" is not a number`} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), "v: "+tag+"\n")
			expectError(t, err, InvalidExpression, "$.v", fragment)
		}
	})
}

func TestUnderscoresSeparateDigitsInEveryNumber(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
fraction: .5_0
exponent: 1e1_0
wide: 1e3_0
decimal: 1.5_0
grouped: 1_000
mixed: -1_000.2_5e0_1
hex: 0x1_F
octal: 0o1_7
binary: -0b1_01
prefixedHex: 0x_ff
prefixedOctal: 0o_17
prefixedBinary: +0b_101
taggedPrefixed: !!int 0x_1_0
taggedFloat: !!float 1e1_0
taggedFraction: !!float .5_0
taggedInt: !!int 1_000
`)
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{
			"fraction": 0.5, "exponent": 1e10, "wide": 1e30, "decimal": 1.5, "grouped": float64(1000), "mixed": -10002.5,
			"hex": float64(31), "octal": float64(15), "binary": float64(-5),
			"prefixedHex": float64(255), "prefixedOctal": float64(15), "prefixedBinary": float64(5), "taggedPrefixed": float64(16),
			"taggedFloat": 1e10, "taggedFraction": 0.5, "taggedInt": float64(1000),
		})
	})
}

func TestMisplacedUnderscoresInNumbersAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, written := range []string{"1_", "1__0", "0x__ff", "0_x10", "0xff_", "1_.5", "1._5", "1_e3", "1e_3", "1e3_", "0_", "-1_", "!!int 1_", "!!float 1e_3", "!!float .5_"} {
			t.Run(written, func(t *testing.T) {
				_, err := sb.MakeExpressionFromJaml(t.Context(), "outer:\n  v: "+written+"\n")
				expectError(t, err, InvalidExpression, "$.outer.v", "underscore")
			})
		}
	})
}

func TestSpellingsOutsideTheNumberGrammarAreText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: 0X1F\nb: 0B101\nc: 0O17\nd: _1\ne: -_1\nf: _\ng: 1.2.3\nh: 0b12\ni: 1e\nj: 0x_\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"a": "0X1F", "b": "0B101", "c": "0O17", "d": "_1", "e": "-_1", "f": "_", "g": "1.2.3", "h": "0b12", "i": "1e", "j": "0x_"})
	})
}

func TestLongAliasedKeysNestedDeepStaySmall(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var text strings.Builder
		text.WriteString("anchor: &k " + strings.Repeat("x", 100_000) + "\nnest:\n")
		for level := 1; level <= 200; level++ {
			text.WriteString(strings.Repeat("  ", level) + "*k :\n")
		}
		text.WriteString(strings.Repeat("  ", 201) + "1\n")
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		if _, err := sb.MakeExpressionFromJaml(t.Context(), text.String()); err != nil {
			t.Fatal(err)
		}
		goruntime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
			t.Fatalf("making the parsed allocated %d bytes", allocated)
		}
	})
}

func TestTheFirstErrorInDocumentOrderIsReported(t *testing.T) {
	var tooDeep = deepTemplate(4097, "1")[len("v: "):]
	for _, text := range []string{"a: " + tooDeep + "b: " + tooDeep, "a: " + tooDeep + "b: {1: x}\n"} {
		for range 50 {
			makeInBothKinds(t, text).expectRefused(t, InvalidExpression, "$.a", "nests deeper than 4096")
		}
	}
}

func TestAliasesExpandToAtMostAMillionValues(t *testing.T) {
	var budget = _AliasBudget{decoded: 2 * maxAliasedValues, aliased: maxAliasedValues}
	if err := budget.spend(true); err == nil || !strings.Contains(err.Error(), "aliases expand to more than 1048576 values") {
		t.Fatalf("got %v", err)
	}
	var unaliased = _AliasBudget{decoded: 2 * maxAliasedValues, aliased: maxAliasedValues}
	if err := unaliased.spend(false); err != nil {
		t.Fatal(err)
	}
}

func TestAnAnchorIsConvertedOnceAndSharedByItsAliases(t *testing.T) {
	root, err := parseYAML(t.Context(), "a: &a {x: [1]}\nb: *a\nc: [*a]\nm: {<<: *a, y: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	var document = root.(*_Mapping)
	var anchored = document.values["a"].(*_Mapping)
	if document.values["b"] != anchored || document.values["c"].([]any)[0] != anchored {
		t.Fatal("an alias holds its own copy of the anchored mapping")
	}
	if merged := document.values["m"].(*_Mapping).values["x"].([]any); &merged[0] != &anchored.values["x"].([]any)[0] {
		t.Fatal("a merge holds its own copy of the anchored values")
	}
}

func wideAliasedDocument() string {
	var text strings.Builder
	text.WriteString("a: &a {")
	for key := range 4000 {
		fmt.Fprintf(&text, "k%d: v%d, ", key, key)
	}
	text.WriteString("}\nrefs:\n")
	for range 98 {
		text.WriteString("- *a\n")
	}
	return text.String()
}

func TestAWideAliasedDocumentResidesAtItsWrittenSize(t *testing.T) {
	var text = wideAliasedDocument()
	var parsed = mustJaml(t, mustSandbox(t, isolatedKind, WithMemoryLimit(16<<20)), text)
	expectEqual(t, evalAs[string](t, parsed, nil, "refs", "97", "k3999"), "v3999")
	expectEqual(t, len(evalAs[map[string]string](t, parsed, nil, "refs", "50")), 4000)
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	root, err := parseYAML(t.Context(), text)
	if err != nil {
		t.Fatal(err)
	}
	goruntime.GC()
	goruntime.ReadMemStats(&after)
	goruntime.KeepAlive(root)
	if retained := int64(after.HeapAlloc) - int64(before.HeapAlloc); retained > 8<<20 {
		t.Fatalf("a %d byte document resides in %d bytes", len(text), retained)
	}
}

func TestSharedAliasesTripTheBudgetAtTheValueThatTipsIt(t *testing.T) {
	var item = "{k0: v,k1: v,k2: v,k3: v,k4: v,k5: v,k6: v,k7: v,k8: v,k9: v}"
	var anchor = "a: &a [" + strings.TrimSuffix(strings.Repeat(item+",", 100), ",") + "]\n"
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), anchor+"b: ["+strings.TrimSuffix(strings.Repeat("*a,", 200), ",")+"]\n")
		expectError(t, err, InvalidExpression, "$.b[99][17].k9: aliases expand to 109198 of the 110301 values read so far, which is excessive aliasing")
		_, err = sb.MakeExpressionFromJaml(t.Context(), anchor+"m: &m {x: *a}\nb: ["+strings.TrimSuffix(strings.Repeat("{<<: *m},", 200), ",")+"]\n")
		expectError(t, err, InvalidExpression, "$.b[107].x[88].k2: aliases expand to 119989 of the 121201 values read so far, which is excessive aliasing")
	})
}

func TestSharedAliasesTripTheDepthLimitAtTheNodeThatTipsIt(t *testing.T) {
	var nested = func(depth int, inner string) string {
		return "v: " + strings.Repeat("[", depth) + inner + strings.Repeat("]", depth) + "\n"
	}
	var deep = func(zeros int, tail string) string {
		return "$.v" + strings.Repeat("[0]", zeros) + tail + ": nests deeper than 4096"
	}
	makeInBothKinds(t, "a: &a [[[1]]]\n"+nested(4092, "*a")).expectAccepted(t)
	makeInBothKinds(t, "a: &a [[[1]]]\n"+nested(4093, "*a")).expectRefused(t, InvalidExpression, deep(4095, ""))
	makeInBothKinds(t, "m: &m {k: {k: {z: 1}}}\n"+nested(4093, "{<<: *m}")).expectRefused(t, InvalidExpression, deep(4093, ".k.k"))
	makeInBothKinds(t, "a: &a [x, &b [y, [z]]]\n"+nested(4092, "[*b, *a]")).expectRefused(t, InvalidExpression, deep(4092, "[1][1][1]"))
	makeInBothKinds(t, "a: &a [x, &b [y, [z]]]\n"+nested(4093, "[*b, *a]")).expectRefused(t, InvalidExpression, deep(4094, "[1]"))
}

func TestABulkAliasChargeTripsExactlyWhenSomeSingleChargeWould(t *testing.T) {
	var random = rand.New(rand.NewPCG(1, 2))
	for range 5_000 {
		var start = _AliasBudget{decoded: random.IntN(5_000_000)}
		var ceiling = min(int(allowedAliasRatio(start.decoded)*float64(start.decoded)), maxAliasedValues, start.decoded)
		start.aliased = max(0, ceiling-random.IntN(10_000))
		var values = 1 + random.IntN(10_000)
		var stepwise, tripped = start, false
		for range values {
			if stepwise.spend(true) != nil {
				tripped = true
				break
			}
		}
		if bulk := start.after(values, true).excess() != nil; bulk != tripped {
			t.Fatalf("from %+v charging %d: bulk trips %v, one at a time trips %v", start, values, bulk, tripped)
		}
	}
}

func TestResolvingASharedAnchorLeavesItForTheNextResolve(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "base: &b {count: \"${ count }\", list: [\"${ count }\"]}\ncopy: *b\nmerged: {<<: *b}\n")
		for _, count := range []int{1, 2, 3} {
			var want = map[string]any{"count": float64(count), "list": []any{float64(count)}}
			expectEqual(t, evalAs[map[string]any](t, parsed, map[string]any{"count": count}), map[string]any{"base": want, "copy": want, "merged": want})
			expectEqual(t, evalAs[map[string]any](t, parsed, map[string]any{"count": count}, "copy"), want)
		}
	})
}

func TestAQuotedMergeKeyNeverCollidesWithAMerge(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
base: &b {x: 1, "<<": merged}
before: {'<<': 1, <<: {b: 2}}
after: {<<: {b: 2}, "<<": 1}
tagged: {!!str <<: 1, <<: *b}
brought: {<<: *b}
own: {"<<": lit, <<: *b}
`)
		var got = evalAs[map[string]map[string]any](t, parsed, nil)
		expectEqual(t, got["before"], map[string]any{"<<": float64(1), "b": float64(2)})
		expectEqual(t, got["after"], map[string]any{"<<": float64(1), "b": float64(2)})
		expectEqual(t, got["tagged"], map[string]any{"<<": float64(1), "x": float64(1)})
		expectEqual(t, got["brought"], map[string]any{"<<": "merged", "x": float64(1)})
		expectEqual(t, got["own"], map[string]any{"<<": "lit", "x": float64(1)})
		_, err := sb.MakeExpressionFromJaml(t.Context(), "m: {'<<': 1, \"<<\": 2}\n")
		expectError(t, err, InvalidExpression, "$.m", `mapping key "<<" already defined at line 1`)
	})
}

func TestANullKeyIsNamedAsNull(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"? \nv: 1\n", "~: 1\n", "null: 1\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "$: the key on line 1 is null; keys must be strings, so quote it")
		}
	})
}
