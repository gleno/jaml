package jaml

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func expectNoFragment(t *testing.T, err error, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("expected no %q in %q", fragment, err.Error())
		}
	}
}

func TestRegexLiteralsEndWhereJavaScriptSaysTheyEnd(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
closing: ${ /}/.test(x) }
opening: ${ /{/.source }
quote: ${ s.split(/'/).length }
slashes: ${ /https?:\/\//.test(u) }
interpolated: "${{ /}}/.test(s) }} and ${{ /{/.source }}"
`)
		var got = evalAs[map[string]any](t, document, map[string]any{"x": "a}", "s": "it's }}", "u": "https://x"})
		expectEqual(t, got, map[string]any{"closing": true, "opening": "{", "quote": float64(2), "slashes": true, "interpolated": "true and {"})
	})
}

func TestInterpolationEndsAtTheFirstClosingPairJavaScriptLeaves(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "object: \"${{ {a: 1}}}\"\nnested: \"<${{ `${ `${1}}` }` }}>\"\nregex: \"${{ /}}/.source }}\"\n")
		var got = evalAs[map[string]string](t, document, nil)
		expectEqual(t, got, map[string]string{"object": `{"a":1}`, "nested": "<1}>", "regex": "}}"})
	})
}

func TestFunctionScopeBindingsDoNotExist(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "count: ${ arguments.length }\ntext: \"${{ arguments }}\"\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("count")), EvaluationFailed, "ReferenceError")
		expectError(t, resolveInto(t.Context(), &out, document.At("text")), EvaluationFailed, "ReferenceError")
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: ${ new.target }\n")
		expectError(t, err, InvalidExpression, "new.target")
	})
}

func TestTopLevelThisIsTheGlobalObject(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: ${ this === globalThis }\n")
		expectEqual(t, mustResolve[bool](t, t.Context(), document.At("a")), true)
	})
}

func TestLegacyOctalEscapesAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{`a: ${ "\101" }`, `a: ${ '\08' }`, `a: '${ ({"\1": 1}) }'`, `a: 'x ${{ "\9" }}'`, `a: ${ 'ok' + "\7" }`} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text+"\n")
			expectError(t, err, InvalidExpression, "Octal escape sequences are not allowed in strict mode")
		}
		var document = mustJaml(t, sb, `a: ${ ["\\101", "\0", "\x41", "\u0041"] }`+"\n")
		expectEqual(t, evalAs[[]string](t, document, nil, "a"), []string{`\101`, "\x00", "A", "A"})
	})
}

func TestACommentOnlyExpressionIsEmpty(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: ${ /* c */ }\n", "a: |\n  ${\n    // c\n  }\n", "a: \"x ${{ /* c */ }}\"\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "empty expression")
		}
	})
}

func TestMessagesNameOnlyTokensTheAuthorWrote(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: ${ 1 + }\n")
		expectError(t, err, InvalidExpression, "Unexpected end of input")
		expectNoFragment(t, err, "Unexpected token )", "use strict")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "a: ${ 1), (2 }\n")
		expectError(t, err, InvalidExpression, "syntax error at line 1, column 3", "Unexpected token )")
	})
}

func TestManyClosingCandidatesStayCheap(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var inString = "a: \"${{ '" + strings.Repeat("}", 200_000) + "' }}\"\n"
		var repeated = "a: \"" + strings.Repeat("${{ '}}}' }} ", 5_000) + "\"\n"
		var unclosed = "a: \"${{ `" + strings.Repeat("}} ", 50_000) + "\"\n"
		for name, text := range map[string]string{"in string": inString, "repeated": repeated, "unclosed": unclosed} {
			var started = time.Now()
			_, _ = sb.MakeExpressionFromJaml(t.Context(), text)
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Fatalf("%s took %v", name, elapsed)
			}
		}
	})
}

func codeValue(unit string, length int) string {
	return "${ " + strings.Repeat(unit, (length-len("${  }"))/len(unit)) + " }"
}

func TestAValueHoldingCodeIsAtMost64KiB(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, written := range []string{codeValue("1+", maxCodeBytes+2), "x ${{ 1 }}" + strings.Repeat(" ", maxCodeBytes)} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), "v: \""+written+"\"\n")
			expectError(t, err, InvalidExpression, "$.v", "longer than 65536 bytes")
		}
		if _, err := compileExpression(strings.Repeat(" ", maxCodeBytes+1)+"1", dollar.value); err == nil {
			t.Fatal("expected the recompile to refuse a long expression")
		}
		var document = mustJaml(t, sb, "plain: \""+strings.Repeat("(", maxCodeBytes+1)+"\"\n")
		expectEqual(t, mustResolve[string](t, t.Context(), document.At("plain")), strings.Repeat("(", maxCodeBytes+1))
	})
}

func TestMillionDeepExpressionTextFailsCleanlyInsteadOfOverflowingTheParser(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		survivesInAStackCappedChild(t, func(t *testing.T) {
			var deep = strings.Repeat("(", 1_500_000)
			for _, text := range []string{"v: ${ " + deep + " }\n", "v: \"x ${{ " + deep + "\"\n", "v: \"x ${{ '}}}' }} " + deep + "\"\n"} {
				_, err := sb.MakeExpressionFromJaml(t.Context(), text)
				expectError(t, err, InvalidExpression, "$.v", "longer than 65536 bytes")
			}
		})
	})
}

func TestDistinctValuesFullOfInterpolationsMakeQuickly(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var written strings.Builder
		for value := range 10 {
			fmt.Fprintf(&written, "v%d: '%d%s'\n", value, value, strings.Repeat("${{a}}", 10_900))
		}
		var started = time.Now()
		var document = mustJaml(t, sb, written.String())
		if elapsed := time.Since(started); elapsed > raceSlowdown*time.Second {
			t.Fatalf("making %d bytes took %v", written.Len(), elapsed)
		}
		expectEqual(t, mustResolve[string](t, valued(t.Context(), map[string]any{"a": 1}), document.At("v3")), "3"+strings.Repeat("1", 10_900))
	}, WithBuildTimeout(30*time.Second))
}

func TestAnEndHiddenBehindClusteredClosersIsStillFound(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var inString = "'}}" + strings.Repeat(" ", 50_000) + strings.Repeat("}}", 5_000) + "'"
		var document = mustJaml(t, sb, "v: \"${{ "+inString+".length }}\"\n")
		expectEqual(t, mustResolve[string](t, t.Context(), document.At("v")), fmt.Sprint(len(inString)-2))
	})
}

func TestAClosedExpressionReportsItsOwnSyntaxError(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for written, fragments := range map[string][]string{
			"v: \"${ `\\\\1` }\"\n":         {"${`\\1`}:", "Octal escape sequences are not allowed in this context"},
			"v: \"a ${{ `\\\\1` }} b\"\n":   {"${{`\\1`}}:", "Octal escape sequences are not allowed in this context"},
			"v: \"${ 1 + `\\\\u` }\"\n":     {"${1 + `\\u`}:", "invalid escape"},
			"v: \"${ /a/z }\"\n":            {"Invalid flags supplied to RegExp constructor 'z'"},
			"v: \"${ /(/ }\"\n":             {"Unterminated group"},
			"v: \"a ${{ /(/ }} b\"\n":       {"${{/(/}}:", "Unterminated group"},
			"v: \"${ '\\\\u{110000}' }\"\n": {"Unexpected token ILLEGAL"},
			"v: \"${ '\\\\x4' }\"\n":        {"Unexpected token ILLEGAL"},
			"v: \"${ 'abc }\"\n":            {"Unexpected token ILLEGAL"},
			"v: \"${ `${1` }\"\n":           {"unclosed ${"},
		} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), written)
			expectError(t, err, InvalidExpression, append([]string{"$.v"}, fragments...)...)
			expectNoFragment(t, err, "unclosed ${{", "}} b")
		}
	})
}
