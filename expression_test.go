package jaml

import (
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
	template := mustTemplate(t, `
closing: ${ /}/.test(x) }
opening: ${ /{/.source }
quote: ${ s.split(/'/).length }
slashes: ${ /https?:\/\//.test(u) }
interpolated: "${{ /}}/.test(s) }} and ${{ /{/.source }}"
`)
	got := evalAs[map[string]any](t, template, map[string]any{"x": "a}", "s": "it's }}", "u": "https://x"})
	expectEqual(t, got, map[string]any{"closing": true, "opening": "{", "quote": float64(2), "slashes": true, "interpolated": "true and {"})
}

func TestInterpolationEndsAtTheFirstClosingPairJavaScriptLeaves(t *testing.T) {
	template := mustTemplate(t, "object: \"${{ {a: 1}}}\"\nnested: \"<${{ `${ `${1}}` }` }}>\"\nregex: \"${{ /}}/.source }}\"\n")
	got := evalAs[map[string]string](t, template, nil)
	expectEqual(t, got, map[string]string{"object": `{"a":1}`, "nested": "<1}>", "regex": "}}"})
}

func TestFunctionScopeBindingsDoNotExist(t *testing.T) {
	template := mustTemplate(t, "count: ${ arguments.length }\ntext: \"${{ arguments }}\"\n")
	var out any
	expectError(t, template.Eval(&out, nil, "count"), EvaluationFailed, "ReferenceError")
	expectError(t, template.Eval(&out, nil, "text"), EvaluationFailed, "ReferenceError")
	_, err := MakeTrustedTemplate("a: ${ new.target }\n")
	expectError(t, err, InvalidTemplate, "new.target")
}

func TestTopLevelThisIsTheGlobalObject(t *testing.T) {
	template := mustTemplate(t, "a: ${ this === globalThis }\n")
	expectEqual(t, evalAs[bool](t, template, nil, "a"), true)
}

func TestLegacyOctalEscapesAreRefused(t *testing.T) {
	for _, text := range []string{`a: ${ "\101" }`, `a: ${ '\08' }`, `a: '${ ({"\1": 1}) }'`, `a: 'x ${{ "\9" }}'`, `a: ${ 'ok' + "\7" }`} {
		_, err := MakeTrustedTemplate(text + "\n")
		expectError(t, err, InvalidTemplate, "Octal escape sequences are not allowed in strict mode")
	}
	template := mustTemplate(t, `a: ${ ["\\101", "\0", "\x41", "\u0041"] }`+"\n")
	expectEqual(t, evalAs[[]string](t, template, nil, "a"), []string{`\101`, "\x00", "A", "A"})
}

func TestACommentOnlyExpressionIsEmpty(t *testing.T) {
	for _, text := range []string{"a: ${ /* c */ }\n", "a: |\n  ${\n    // c\n  }\n", "a: \"x ${{ /* c */ }}\"\n"} {
		_, err := MakeTrustedTemplate(text)
		expectError(t, err, InvalidTemplate, "empty expression")
	}
}

func TestMessagesNameOnlyTokensTheAuthorWrote(t *testing.T) {
	_, err := MakeTrustedTemplate("a: ${ 1 + }\n")
	expectError(t, err, InvalidTemplate, "Unexpected end of input")
	expectNoFragment(t, err, "Unexpected token )", "use strict")
	_, err = MakeTrustedTemplate("a: ${ 1), (2 }\n")
	expectError(t, err, InvalidTemplate, "syntax error at line 1, column 3", "Unexpected token )")
}

func TestManyClosingCandidatesStayCheap(t *testing.T) {
	inString := "a: \"${{ '" + strings.Repeat("}", 200_000) + "' }}\"\n"
	repeated := "a: \"" + strings.Repeat("${{ '}}}' }} ", 5_000) + "\"\n"
	unclosed := "a: \"${{ `" + strings.Repeat("}} ", 50_000) + "\"\n"
	for name, text := range map[string]string{"in string": inString, "repeated": repeated, "unclosed": unclosed} {
		started := time.Now()
		_, _ = MakeTrustedTemplate(text)
		if took := time.Since(started); took > 10*time.Second {
			t.Fatalf("%s took %v", name, took)
		}
	}
}

func TestWindowedEndSearchAgreesWithOneParseOfTheRest(t *testing.T) {
	corpus := []string{
		" 1 }} tail }}", " '}}' }}", " {a: 1}}}", " `${ {b: '}}'}.b }` }} x }}", " /}}/.test(s) }} }}",
		" x // }}\n }} y }}", " /* }} */ 1 }}", " a }x}}", " 1}", " ) }}", " (1 }} }}", " '}}}' }} '}}' }}",
		" x => { return {a: '}}'} } }} }}", " [1, {b: 2}]}}}}",
	}
	for _, after := range corpus {
		windowed, windowedErr := expressionEnd(after, interpolation)
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
	for _, written := range []string{codeValue("1+", maxCodeBytes+2), "x ${{ 1 }}" + strings.Repeat(" ", maxCodeBytes)} {
		_, err := MakeTrustedTemplate("v: \"" + written + "\"\n")
		expectError(t, err, InvalidTemplate, "$.v", "longer than 65536 bytes")
	}
	if _, err := compileExpression(strings.Repeat(" ", maxCodeBytes+1)+"1", false); err == nil {
		t.Fatal("expected the recompile to refuse a long expression")
	}
	template := mustTemplate(t, "plain: \""+strings.Repeat("(", maxCodeBytes+1)+"\"\n")
	expectEqual(t, evalAs[string](t, template, nil, "plain"), strings.Repeat("(", maxCodeBytes+1))
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
	survivesInAStackCappedChild(t, func(t *testing.T) {
		deep := strings.Repeat("(", 1_500_000)
		for _, text := range []string{"v: ${ " + deep + " }\n", "v: \"x ${{ " + deep + "\"\n", "v: \"x ${{ '}}}' }} " + deep + "\"\n"} {
			_, err := MakeTrustedTemplate(text)
			expectError(t, err, InvalidTemplate, "$.v", "longer than 65536 bytes")
		}
	})
}
