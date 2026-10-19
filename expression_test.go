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
		template := mustTemplate(t, sb, `
closing: ${ /}/.test(x) }
opening: ${ /{/.source }
quote: ${ s.split(/'/).length }
slashes: ${ /https?:\/\//.test(u) }
interpolated: "${{ /}}/.test(s) }} and ${{ /{/.source }}"
`)
		got := evalAs[map[string]any](t, template, map[string]any{"x": "a}", "s": "it's }}", "u": "https://x"})
		expectEqual(t, got, map[string]any{"closing": true, "opening": "{", "quote": float64(2), "slashes": true, "interpolated": "true and {"})
	})
}

func TestInterpolationEndsAtTheFirstClosingPairJavaScriptLeaves(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "object: \"${{ {a: 1}}}\"\nnested: \"<${{ `${ `${1}}` }` }}>\"\nregex: \"${{ /}}/.source }}\"\n")
		got := evalAs[map[string]string](t, template, nil)
		expectEqual(t, got, map[string]string{"object": `{"a":1}`, "nested": "<1}>", "regex": "}}"})
	})
}

func TestFunctionScopeBindingsDoNotExist(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "count: ${ arguments.length }\ntext: \"${{ arguments }}\"\n")
		var out any
		expectError(t, template.Eval(t.Context(), &out, nil, "count"), EvaluationFailed, "ReferenceError")
		expectError(t, template.Eval(t.Context(), &out, nil, "text"), EvaluationFailed, "ReferenceError")
		_, err := sb.MakeTemplate(t.Context(), "a: ${ new.target }\n")
		expectError(t, err, InvalidTemplate, "new.target")
	})
}

func TestTopLevelThisIsTheGlobalObject(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: ${ this === globalThis }\n")
		expectEqual(t, evalAs[bool](t, template, nil, "a"), true)
	})
}

func TestLegacyOctalEscapesAreRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{`a: ${ "\101" }`, `a: ${ '\08' }`, `a: '${ ({"\1": 1}) }'`, `a: 'x ${{ "\9" }}'`, `a: ${ 'ok' + "\7" }`} {
			_, err := sb.MakeTemplate(t.Context(), text+"\n")
			expectError(t, err, InvalidTemplate, "Octal escape sequences are not allowed in strict mode")
		}
		template := mustTemplate(t, sb, `a: ${ ["\\101", "\0", "\x41", "\u0041"] }`+"\n")
		expectEqual(t, evalAs[[]string](t, template, nil, "a"), []string{`\101`, "\x00", "A", "A"})
	})
}

func TestACommentOnlyExpressionIsEmpty(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: ${ /* c */ }\n", "a: |\n  ${\n    // c\n  }\n", "a: \"x ${{ /* c */ }}\"\n"} {
			_, err := sb.MakeTemplate(t.Context(), text)
			expectError(t, err, InvalidTemplate, "empty expression")
		}
	})
}

func TestMessagesNameOnlyTokensTheAuthorWrote(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeTemplate(t.Context(), "a: ${ 1 + }\n")
		expectError(t, err, InvalidTemplate, "Unexpected end of input")
		expectNoFragment(t, err, "Unexpected token )", "use strict")
		_, err = sb.MakeTemplate(t.Context(), "a: ${ 1), (2 }\n")
		expectError(t, err, InvalidTemplate, "syntax error at line 1, column 3", "Unexpected token )")
	})
}

func TestManyClosingCandidatesStayCheap(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		inString := "a: \"${{ '" + strings.Repeat("}", 200_000) + "' }}\"\n"
		repeated := "a: \"" + strings.Repeat("${{ '}}}' }} ", 5_000) + "\"\n"
		unclosed := "a: \"${{ `" + strings.Repeat("}} ", 50_000) + "\"\n"
		for name, text := range map[string]string{"in string": inString, "repeated": repeated, "unclosed": unclosed} {
			started := time.Now()
			_, _ = sb.MakeTemplate(t.Context(), text)
			if took := time.Since(started); took > 10*time.Second {
				t.Fatalf("%s took %v", name, took)
			}
		}
	})
}

func TestWindowedEndSearchAgreesWithOneParseOfTheRest(t *testing.T) {
	corpus := []string{
		" 1 }} tail }}", " '}}' }}", " {a: 1}}}", " `${ {b: '}}'}.b }` }} x }}", " /}}/.test(s) }} }}",
		" x // }}\n }} y }}", " /* }} */ 1 }}", " a }x}}", " 1}", " ) }}", " (1 }} }}", " '}}}' }} '}}' }}",
		" x => { return {a: '}}'} } }} }}", " [1, {b: 2}]}}}}",
		" `\\1` }} b }}", " 1 + `\\u` }} }}",
	}
	for _, after := range corpus {
		windowed, windowedErr := searchingEndsIn(after).expressionEnd(after, interpolation)
		whole, wholeErr := finalEnd(after, interpolation)
		if windowed != whole || (windowedErr == nil) != (wholeErr == nil) {
			t.Fatalf("%q: windowed %d %v, whole %d %v", after, windowed, windowedErr, whole, wholeErr)
		}
	}
}

func codeValue(unit string, length int) string {
	return "${ " + strings.Repeat(unit, (length-len("${  }"))/len(unit)) + " }"
}

func TestAValueHoldingCodeIsAtMost64KiB(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, written := range []string{codeValue("1+", maxCodeBytes+2), "x ${{ 1 }}" + strings.Repeat(" ", maxCodeBytes)} {
			_, err := sb.MakeTemplate(t.Context(), "v: \""+written+"\"\n")
			expectError(t, err, InvalidTemplate, "$.v", "longer than 65536 bytes")
		}
		if _, err := compileExpression(strings.Repeat(" ", maxCodeBytes+1)+"1", wholeValue); err == nil {
			t.Fatal("expected the recompile to refuse a long expression")
		}
		template := mustTemplate(t, sb, "plain: \""+strings.Repeat("(", maxCodeBytes+1)+"\"\n")
		expectEqual(t, evalAs[string](t, template, nil, "plain"), strings.Repeat("(", maxCodeBytes+1))
	})
}

func TestTheWorstRecursiveConstructsUnderTheCapLeaveTheParserStackBounded(t *testing.T) {
	survivesInAChildWithMaxStack(t, 256<<20, func(t *testing.T) {
		for _, unit := range []string{"(", "[", "{", "a?", "x=>", "(x)=>", "!", "-", "typeof ", "void ", "new ", "a=", "`${"} {
			for _, written := range []string{codeValue(unit, maxCodeBytes), "x ${{ " + strings.Repeat(unit, (maxCodeBytes-6)/len(unit))} {
				if len(written) > maxCodeBytes {
					t.Fatalf("%q: test value is %d bytes", unit, len(written))
				}
				c := &compiler{texts: map[string]text{}}
				_, _ = c.compileText(written)
			}
			c := &compiler{texts: map[string]text{}}
			if _, err := c.compileText(codeValue(unit, maxCodeBytes+len(unit)+1)); err == nil || !strings.Contains(err.Error(), "longer than") {
				t.Fatalf("%q: expected the size refusal, got %v", unit, err)
			}
		}
	})
}

func TestMillionDeepExpressionTextFailsCleanlyInsteadOfOverflowingTheParser(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		survivesInAStackCappedChild(t, func(t *testing.T) {
			deep := strings.Repeat("(", 1_500_000)
			for _, text := range []string{"v: ${ " + deep + " }\n", "v: \"x ${{ " + deep + "\"\n", "v: \"x ${{ '}}}' }} " + deep + "\"\n"} {
				_, err := sb.MakeTemplate(t.Context(), text)
				expectError(t, err, InvalidTemplate, "$.v", "longer than 65536 bytes")
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
		started := time.Now()
		template := mustTemplate(t, sb, written.String())
		if took := time.Since(started); took > raceSlowdown*time.Second {
			t.Fatalf("making %d bytes took %v", written.Len(), took)
		}
		expectEqual(t, evalAs[string](t, template, map[string]any{"a": 1}, "v3"), "3"+strings.Repeat("1", 10_900))
	}, WithBuildTimeout(30*time.Second))
}

func TestAnEndHiddenBehindClusteredClosersIsStillFound(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		inString := "'}}" + strings.Repeat(" ", 50_000) + strings.Repeat("}}", 5_000) + "'"
		template := mustTemplate(t, sb, "v: \"${{ "+inString+".length }}\"\n")
		expectEqual(t, evalAs[string](t, template, nil, "v"), fmt.Sprint(len(inString)-2))
	})
}

func TestAnEndSearchThatOutgrowsItsValueIsRefused(t *testing.T) {
	after := " `}}" + strings.Repeat(" ", 1_000) + strings.Repeat("}}", 100) + "` }}"
	search := &endSearch{unparsed: 4 * len(after)}
	if _, err := search.expressionEnd(after, interpolation); err != errCostlyEnds {
		t.Fatalf("expected the costly-ends refusal, got %v", err)
	}
	if end, err := searchingEndsIn(after).expressionEnd(after, interpolation); err != nil || end != len(after)-2 {
		t.Fatalf("expected the end at %d, got %d %v", len(after)-2, end, err)
	}
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
			_, err := sb.MakeTemplate(t.Context(), written)
			expectError(t, err, InvalidTemplate, append([]string{"$.v"}, fragments...)...)
			expectNoFragment(t, err, "unclosed ${{", "}} b")
		}
	})
}
