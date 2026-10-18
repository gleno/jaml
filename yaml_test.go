package jaml

import (
	"math/big"
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
