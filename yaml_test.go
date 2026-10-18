package jaml

import (
	"fmt"
	"math/big"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestOnlyTrueAndFalseAreBools(t *testing.T) {
	template := mustTemplate(t, "y: y\nyes: yes\non: on\nn: n\ntruth: true\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"y": "y", "yes": "yes", "on": "on", "n": "n", "truth": true})
}

func TestNonStringKeysAreRefused(t *testing.T) {
	cases := map[string]string{
		"integer": "1: x\n",
		"bool":    "outer:\n  true: x\n",
		"null":    "~: x\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := MakeTrustedTemplate(text)
			expectError(t, err, InvalidTemplate, "keys must be strings")
		})
	}
	_, err := MakeTrustedTemplate("outer:\n  true: x\n")
	expectError(t, err, InvalidTemplate, "$.outer: key true is a bool")
}

func TestQuotedKeysAreStrings(t *testing.T) {
	got := evalAs[map[string]string](t, mustTemplate(t, "\"1\": x\n\"true\": y\n"), nil)
	expectEqual(t, got, map[string]string{"1": "x", "true": "y"})
}

func TestInfinityAndNaNAreRefused(t *testing.T) {
	for _, text := range []string{"a: .inf\n", "a: -.inf\n", "a: [.nan]\n"} {
		_, err := MakeTrustedTemplate(text)
		expectError(t, err, InvalidTemplate, "not a finite number")
	}
}

func TestDuplicateKeysAreRefused(t *testing.T) {
	_, err := MakeTrustedTemplate("a: 1\na: 2\n")
	expectError(t, err, InvalidTemplate, `mapping key "a" already defined at line 1`)
}

func TestDatesKeepTheirSpelling(t *testing.T) {
	type shape struct {
		At     time.Time `json:"at"`
		Plain  string    `json:"plain"`
		Quoted string    `json:"quoted"`
		Tagged string    `json:"tagged"`
	}
	template := mustTemplate(t, "at: 2026-10-18T10:00:00Z\nplain: 2026-10-18\nquoted: \"2026-10-18\"\ntagged: !!timestamp 2026-10-18 10:00:00\n")
	got := evalAs[shape](t, template, nil)
	expectEqual(t, got, shape{
		At:     time.Date(2026, time.October, 18, 10, 0, 0, 0, time.UTC),
		Plain:  "2026-10-18",
		Quoted: "2026-10-18",
		Tagged: "2026-10-18 10:00:00",
	})
	dates := evalAs[map[string]any](t, mustTemplate(t, "base: &d 2026-10-18\ncopy: *d\nlist: [2026-01-02]\n"), nil)
	expectEqual(t, dates, map[string]any{"base": "2026-10-18", "copy": "2026-10-18", "list": []any{"2026-01-02"}})
}

func TestMergeKeysMerge(t *testing.T) {
	template := mustTemplate(t, "base: &b {x: 1}\nm:\n  <<: *b\n  y: \"${ 1 + 1 }\"\n")
	expectEqual(t, evalAs[map[string]int](t, template, nil, "m"), map[string]int{"x": 1, "y": 2})
}

func TestASecondDocumentIsRefused(t *testing.T) {
	_, err := MakeTrustedTemplate("a: 1\n---\nb: 2\n")
	expectError(t, err, InvalidTemplate, "line 2 starts a second YAML document")
	got := evalAs[map[string]int](t, mustTemplate(t, "---\na: 1\n---\n"), nil)
	expectEqual(t, got, map[string]int{"a": 1})
}

func TestIntegersBeyondInt64AreText(t *testing.T) {
	template := mustTemplate(t, `
max: 9223372036854775807
over: 9223372036854775808
under: -9223372036854775809
huge: 123456789012345678901234567890
hex: 0x8000000000000000
grouped: 1_000
18446744073709551616: key
`)
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{
		"max":                  float64(9223372036854775807),
		"over":                 "9223372036854775808",
		"under":                "-9223372036854775809",
		"huge":                 "123456789012345678901234567890",
		"hex":                  "0x8000000000000000",
		"grouped":              float64(1000),
		"18446744073709551616": "key",
	})
	expectEqual(t, evalAs[int64](t, template, nil, "max"), int64(9223372036854775807))
	var huge big.Int
	if err := huge.UnmarshalText([]byte(evalAs[string](t, template, nil, "huge"))); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, huge.String(), "123456789012345678901234567890")
}

func TestLegacyOctalStaysText(t *testing.T) {
	template := mustTemplate(t, "legacy: 0644\nnegative: -0755\nexplicit: 0o644\nzero: 0\ntagged: !!int 0644\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"legacy": "0644", "negative": "-0755", "explicit": float64(420), "zero": float64(0), "tagged": float64(420)})
	var mode int
	expectError(t, template.Eval(&mode, nil, "legacy"), CoercionFailed, "$.legacy", "cannot unmarshal string")
}

func TestOutOfRangeFloatsAreRefused(t *testing.T) {
	for _, text := range []string{"v: 0.1e400\n", "v: [-1e999]\n", "v: {deep: 1_0e999}\n"} {
		_, err := MakeTrustedTemplate(text)
		expectError(t, err, InvalidTemplate, "out of the range of a 64-bit float")
	}
	_, err := MakeTrustedTemplate("outer:\n  v: 1e400\n")
	expectError(t, err, InvalidTemplate, "$.outer.v: 1e400")
	got := evalAs[map[string]any](t, mustTemplate(t, "quoted: \"1e400\"\ntiny: 1e-400\nbig: 1e308\n"), nil)
	expectEqual(t, got, map[string]any{"quoted": "1e400", "tiny": float64(0), "big": 1e308})
}

func TestIntegersUpToInt64KeepTheirDigits(t *testing.T) {
	expectEqual(t, evalAs[int64](t, mustTemplate(t, "v: 9007199254740993\n"), nil, "v"), int64(9007199254740993))
}

func TestAnEmptyDocumentIsNull(t *testing.T) {
	out := 5
	if err := mustTemplate(t, "").Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, 5)
}

func TestInvalidYAMLFailsAtMakeTrustedTemplate(t *testing.T) {
	_, err := MakeTrustedTemplate("a: [1, 2\n")
	expectError(t, err, InvalidTemplate)
}

func TestAliasesExpandWithTheirExpressions(t *testing.T) {
	template := mustTemplate(t, "base: &b {count: \"${ count }\"}\ncopy: *b\n")
	got := evalAs[map[string]map[string]int](t, template, map[string]any{"count": 4})
	expectEqual(t, got, map[string]map[string]int{"base": {"count": 4}, "copy": {"count": 4}})
}

func TestCommentsDoNotLeakIntoValues(t *testing.T) {
	template := mustTemplate(t, "a: ${1+1} # two\nb: \"# not a comment\"\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": float64(2), "b": "# not a comment"})
}

func TestUnderscoredLegacyOctalStaysText(t *testing.T) {
	template := mustTemplate(t, "a: 0_644\nb: -0_644\nc: 0_89\nd: +0_7\ngrouped: 1_000\nhex: 0x_ff\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": "0_644", "b": "-0_644", "c": "0_89", "d": "+0_7", "grouped": float64(1000), "hex": float64(255)})
}

func TestBinaryBecomesItsDecodedText(t *testing.T) {
	got := evalAs[map[string]any](t, mustTemplate(t, "v: !!binary aGk=\n!!binary YQ==: key\n"), nil)
	expectEqual(t, got, map[string]any{"v": "hi", "a": "key"})
}

func TestBinaryThatIsNotUTF8IsRefused(t *testing.T) {
	_, err := MakeTrustedTemplate("outer:\n  v: !!binary /w==\n")
	expectError(t, err, InvalidTemplate, "$.outer.v", "UTF-8")
	_, err = MakeTrustedTemplate("!!binary /w==: 1\n")
	expectError(t, err, InvalidTemplate, "$", "UTF-8")
	_, err = MakeTrustedTemplate("v: !!binary '!!'\n")
	expectError(t, err, InvalidTemplate, "$.v", "base64")
}

func TestBinaryKeysAreCheckedForDuplicates(t *testing.T) {
	_, err := MakeTrustedTemplate("a: 1\n!!binary YQ==: 2\n")
	expectError(t, err, InvalidTemplate, `mapping key "a" already defined at line 1`)
}

func TestErrorsUnderMergeKeysNameTheDataPath(t *testing.T) {
	_, err := MakeTrustedTemplate("x:\n  <<: {a: 1e400}\n")
	expectError(t, err, InvalidTemplate, "$.x.a: 1e400")
	_, err = MakeTrustedTemplate("x:\n  <<: {a: !!binary /w==}\n")
	expectError(t, err, InvalidTemplate, "$.x.a")
	if strings.Contains(err.Error(), "<<") {
		t.Fatalf("path runs through the merge key: %v", err)
	}
}

func TestMergeKeysFollowTheMergeSpec(t *testing.T) {
	template := mustTemplate(t, `
a: &a {k: 1, p: a, deep: {x: 1}}
b: &b {k: 2, q: b, r: b}
m:
  r: own
  <<: [*a, *b]
inline:
  <<: {z: 1}
  z: 2
`)
	expectEqual(t, evalAs[map[string]any](t, template, nil, "m"), map[string]any{"k": float64(1), "p": "a", "q": "b", "r": "own", "deep": map[string]any{"x": float64(1)}})
	expectEqual(t, evalAs[map[string]any](t, template, nil, "inline"), map[string]any{"z": float64(2)})
	for _, text := range []string{"m:\n  <<: 1\n", "a: &a [1]\nm:\n  <<: *a\n", "m:\n  <<: [1]\n"} {
		_, err := MakeTrustedTemplate(text)
		expectError(t, err, InvalidTemplate, "$.m", "merge")
	}
	_, err := MakeTrustedTemplate("m:\n  <<: {a: 1}\n  <<: {b: 1}\n")
	expectError(t, err, InvalidTemplate, "$.m", `mapping key "<<" already defined at line 2`)
}

func TestQuotedMergeKeyIsAPlainKey(t *testing.T) {
	got := evalAs[map[string]any](t, mustTemplate(t, "\"<<\": {a: 1}\n"), nil)
	expectEqual(t, got, map[string]any{"<<": map[string]any{"a": float64(1)}})
}

func TestAliasBombsAreRefused(t *testing.T) {
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
	_, err := MakeTrustedTemplate(text.String())
	expectError(t, err, InvalidTemplate, "aliases expand to more than 1048576 values")
}

func TestScalarTagsResolveByJamlRules(t *testing.T) {
	template := mustTemplate(t, "s: !!str 12\ni: !!int 0644\nf: !!float 3\nb: !!bool true\nn: !!null ~\ncustom: !thing 12\nquoted: '12'\nnul: null\ntilde: ~\nempty:\ncapital: True\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"s": "12", "i": float64(420), "f": float64(3), "b": true, "n": nil, "custom": "12", "quoted": "12", "nul": nil, "tilde": nil, "empty": nil, "capital": true})
	for _, text := range []string{"v: !!int x\n", "v: !!int 089\n", "v: !!float x\n", "v: !!bool yes\n", "v: !!null x\n"} {
		_, err := MakeTrustedTemplate(text)
		expectError(t, err, InvalidTemplate, "$.v")
	}
}

func TestLongAliasedKeysNestedDeepStaySmall(t *testing.T) {
	var text strings.Builder
	text.WriteString("anchor: &k " + strings.Repeat("x", 100_000) + "\nnest:\n")
	for level := 1; level <= 200; level++ {
		text.WriteString(strings.Repeat("  ", level) + "*k :\n")
	}
	text.WriteString(strings.Repeat("  ", 201) + "1\n")
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	if _, err := MakeTrustedTemplate(text.String()); err != nil {
		t.Fatal(err)
	}
	goruntime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Fatalf("making the template allocated %d bytes", allocated)
	}
}

func TestTheFirstErrorInDocumentOrderIsReported(t *testing.T) {
	tooDeep := deepTemplate(4097, "1")[len("v: "):]
	for _, text := range []string{"a: " + tooDeep + "b: " + tooDeep, "a: " + tooDeep + "b: {1: x}\n"} {
		for range 50 {
			makeInBothModes(t, text).expectRefused(t, InvalidTemplate, "$.a", "nests deeper than 4096")
		}
	}
}
