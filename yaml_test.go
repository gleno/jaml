package jaml

import (
	"strings"
	"testing"
)

func TestTheNorwayProblemDoesNotHappen(t *testing.T) {
	template := mustTemplate(t, "countries: [no, se, NO]\nanswers: {yes: y, on: off, n: N}\n")
	type shape struct {
		Countries []string          `json:"countries"`
		Answers   map[string]string `json:"answers"`
	}
	got := evalAs[shape](t, template, nil)
	expectEqual(t, got, shape{Countries: []string{"no", "se", "NO"}, Answers: map[string]string{"yes": "y", "on": "off", "n": "N"}})
	expectEqual(t, evalAs[map[string]any](t, template, nil)["answers"], map[string]any{"yes": "y", "on": "off", "n": "N"})
}

func TestScalarsIntoTextKeepTheirSpelling(t *testing.T) {
	template := mustTemplate(t, "version: 1.10\nzip: 02134\nhex: 0xFF\nexp: 1E3\nbig: 123456789012345678901234567890\nday: 2001-12-14\n")
	got := evalAs[map[string]string](t, template, nil)
	expectEqual(t, got, map[string]string{"version": "1.10", "zip": "02134", "hex": "0xFF", "exp": "1E3", "big": "123456789012345678901234567890", "day": "2001-12-14"})
}

func TestLeadingZeroIntegersAreAmbiguousOutsideText(t *testing.T) {
	template := mustTemplate(t, "mode: 0644\n")
	var number int
	expectError(t, template.Eval(&number, nil, "mode"), CoercionFailed, "$.mode (line 1, column 7)", "0644 is ambiguous", "0o644")
	var anything any
	expectError(t, template.Eval(&anything, nil, "mode"), CoercionFailed, "ambiguous")
	expectEqual(t, evalAs[int](t, mustTemplate(t, "mode: 0o644\n"), nil, "mode"), 0o644)
	expectEqual(t, evalAs[int](t, mustTemplate(t, "zero: 0\nnegative: -0\n"), nil, "negative"), 0)
}

func TestInfinityAndNaNOnlyCoerceToText(t *testing.T) {
	template := mustTemplate(t, "a: .inf\nb: -.Inf\nc: .nan\n")
	expectEqual(t, evalAs[map[string]string](t, template, nil), map[string]string{"a": ".inf", "b": "-.Inf", "c": ".nan"})
	var out map[string]float64
	expectError(t, template.Eval(&out, nil), CoercionFailed, "not a finite number")
	var text string
	expectError(t, mustTemplate(t, "v: [.inf]\n").Eval(&text, nil, "v"), CoercionFailed, "$.v[0]", "not a finite number")
}

func TestOnlyYAML12CoreScalarsAreTyped(t *testing.T) {
	template := mustTemplate(t, "a: 1_000\nb: 0b101\nc: True\nd: NULL\ne: +12\nf: -1.5e-3\ng: 1.\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": "1_000", "b": "0b101", "c": true, "d": nil, "e": int64(12), "f": -0.0015, "g": int64(1)})
}

func TestExplicitTagsAreHonoured(t *testing.T) {
	template := mustTemplate(t, "a: !!str 123\nb: !!int \"42\"\nc: !!float 3\nd: !!bool 'true'\ne: !!null ''\nf: !!map {x: 1}\ng: !!seq [1]\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": "123", "b": int64(42), "c": int64(3), "d": true, "e": nil, "f": map[string]any{"x": int64(1)}, "g": []any{int64(1)}})
}

func TestInvalidYAMLFailsAtMakeTemplate(t *testing.T) {
	cases := map[string]struct {
		text      string
		fragments []string
	}{
		"malformed":          {"a: [1, 2\n", []string{"yaml:"}},
		"tabs":               {"a:\n\tb: 1\n", []string{"yaml:"}},
		"unquoted object":    {"a: ${ {b: 1} }\n", []string{"mapping values are not allowed"}},
		"duplicate key":      {"a: 1\nb: 2\na: 3\n", []string{"$.a (line 3, column 1): duplicate key"}},
		"two documents":      {"a: 1\n---\nb: 2\n", []string{"one YAML document"}},
		"merge key":          {"base: &b {x: 1}\nchild:\n  <<: *b\n", []string{"merge keys"}},
		"unknown tag":        {"a: !custom x\n", []string{"unsupported tag !custom"}},
		"binary tag":         {"a: !!binary aGk=\n", []string{"unsupported tag !!binary"}},
		"set tag":            {"a: !!set {x}\n", []string{"unsupported tag !!set"}},
		"bad int tag":        {"a: !!int abc\n", []string{`"abc" is not a valid !!int`}},
		"bad bool tag":       {"a: !!bool yes\n", []string{`"yes" is not a valid !!bool`}},
		"fractional int tag": {"a: !!int 1.5\n", []string{"not a valid !!int"}},
		"complex key":        {"? [a, b]\n: 1\n", []string{"mapping keys must be scalars"}},
		"unclosed in a list": {"a: [\"${{x\"]\n", []string{"$.a[0]", "unclosed ${{"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := MakeTemplate(c.text)
			expectError(t, err, InvalidTemplate, c.fragments...)
		})
	}
}

func TestAnEmptyDocumentIsNull(t *testing.T) {
	for _, text := range []string{"", "# just a comment\n", "---\n"} {
		template := mustTemplate(t, text)
		expectEqual(t, evalAs[any](t, template, nil), nil)
		var out struct{ A int }
		expectError(t, mustTemplate(t, text, WithNullCoercion(Strict)).Eval(&out, nil), CoercionFailed, "strict null")
	}
}

func TestATopLevelScalarOrListIsATemplate(t *testing.T) {
	expectEqual(t, evalAs[int](t, mustTemplate(t, "${40 + 2}"), nil), 42)
	expectEqual(t, evalAs[[]string](t, mustTemplate(t, "- a\n- ${'b'}\n"), nil), []string{"a", "b"})
}

func TestAliasesExpandWithTheirExpressions(t *testing.T) {
	template := mustTemplate(t, "base: &base\n  port: ${port}\n  host: h\nfirst: *base\nsecond: *base\nlist: [*base]\n")
	got := evalAs[map[string]any](t, template, map[string]any{"port": 80})
	want := map[string]any{"port": int64(80), "host": "h"}
	expectEqual(t, got, map[string]any{"base": want, "first": want, "second": want, "list": []any{want}})
}

func TestAliasedKeysAreAllowed(t *testing.T) {
	template := mustTemplate(t, "names: [&k key]\nmap:\n  *k : 1\n")
	expectEqual(t, evalAs[map[string]int](t, template, nil, "map"), map[string]int{"key": 1})
}

func TestAliasBombsAreRefused(t *testing.T) {
	var text strings.Builder
	text.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i <= 9; i++ {
		text.WriteString("a" + string(rune('0'+i)) + ": &a" + string(rune('0'+i)) + " [")
		for j := 0; j < 10; j++ {
			if j > 0 {
				text.WriteString(", ")
			}
			text.WriteString("*a" + string(rune('0'+i-1)))
		}
		text.WriteString("]\n")
	}
	_, err := MakeTemplate(text.String())
	expectError(t, err, InvalidTemplate, "aliases expand the template past")
}

func TestMultiLineStrings(t *testing.T) {
	template := mustTemplate(t, `
literal: |
  one
  two ${{n}}
stripped: |-
  one
folded: >-
  one
  two
plain: one
  two
quoted: "one
  two ${{n}}"
`)
	got := evalAs[map[string]string](t, template, map[string]any{"n": 2})
	expectEqual(t, got, map[string]string{"literal": "one\ntwo 2\n", "stripped": "one", "folded": "one two", "plain": "one two", "quoted": "one two 2"})
}

func TestCommentsAndAnchorsDoNotLeakIntoValues(t *testing.T) {
	template := mustTemplate(t, "a: ${1 + 1} # two\nb: &x '#not a comment'\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": int64(2), "b": "#not a comment"})
}
