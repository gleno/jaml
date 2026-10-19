package jaml

import "testing"

func TestAWholeExpressionKeepsItsType(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
number: ${1+1}
fraction: ${1/4}
text: ${'a' + 'b'}
truth: ${1 < 2}
nothing: ${null}
list: ${[1, 'two', true]}
object: "${ {a: 1, b: [2]} }"
`)
		got := evalAs[map[string]any](t, template, nil)
		expectEqual(t, got, map[string]any{
			"number":   float64(2),
			"fraction": 0.25,
			"text":     "ab",
			"truth":    true,
			"nothing":  nil,
			"list":     []any{float64(1), "two", true},
			"object":   map[string]any{"a": float64(1), "b": []any{float64(2)}},
		})
	})
}

func TestQuotingDoesNotChangeAWholeExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "plain: ${1+1}\ndouble: \"${1+1}\"\nsingle: '${1+1}'\n")
		got := evalAs[map[string]any](t, template, nil)
		expectEqual(t, got, map[string]any{"plain": float64(2), "double": float64(2), "single": float64(2)})
	})
}

func TestWhitespaceAroundAWholeExpressionIsIgnored(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: \"  ${ 1 + 1 }  \"\n")
		expectEqual(t, evalAs[int](t, template, nil, "a"), 2)
	})
}

func TestInterpolationAlwaysMakesText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
alone: ${{1+1}}
mixed: "n=${{n}}, twice=${{n*2}}!"
list: "items: ${{[1, 'a', null]}}"
object: "o: ${{ {k: 'v'} }}"
truth: "${{true}} and ${{1 > 2}}"
`)
		got := evalAs[map[string]any](t, template, map[string]any{"n": 21})
		expectEqual(t, got, map[string]any{
			"alone":  "2",
			"mixed":  "n=21, twice=42!",
			"list":   `items: [1,"a",null]`,
			"object": `o: {"k":"v"}`,
			"truth":  "true and false",
		})
	})
}

func TestInterpolationKeepsSurroundingWhitespace(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: \"  x ${{1}}  \"\n")
		expectEqual(t, evalAs[string](t, template, nil, "a"), "  x 1  ")
	})
}

func TestNumbersInterpolateAsJavaScriptWritesThem(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: \"${{1e21}} ${{0.1+0.2}} ${{-0}} ${{2**53}} ${{1e-7}}\"\n")
		expectEqual(t, evalAs[string](t, template, nil, "a"), "1e+21 0.30000000000000004 0 9007199254740992 1e-7")
	})
}

func TestBracesInsideStringsTemplatesAndCommentsDoNotCloseTheExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
quoted: "${ '}' + \"}\" }"
templated: "${ `+"`a${ {b: '}'}.b }c`"+` }"
interpolated: "x${{ '}}' }}y"
commented: |
  ${
    // a } brace in a comment
    /* and } here */
    1 + 1
  }
`)
		got := evalAs[map[string]any](t, template, nil)
		expectEqual(t, got, map[string]any{"quoted": "}}", "templated": "a}c", "interpolated": "x}}y", "commented": float64(2)})
	})
}

func TestAJavaScriptStringWritesLiteralDelimiters(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: \"${{'${{'}}not code}}\"\nb: ${'${x}'}\n")
		got := evalAs[map[string]string](t, template, nil)
		expectEqual(t, got, map[string]string{"a": "${{not code}}", "b": "${x}"})
	})
}

func TestSingleBracesInsideTextAreLiteral(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: echo ${HOME}/bin\n")
		expectEqual(t, evalAs[string](t, template, nil, "a"), "echo ${HOME}/bin")
	})
}

func TestKeysAreNeverEvaluated(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "${1+1}: a\n\"${{x}}\": b\n")
		got := evalAs[map[string]string](t, template, nil)
		expectEqual(t, got, map[string]string{"${1+1}": "a", "${{x}}": "b"})
	})
}

func TestBlockScalarsHoldExpressions(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
literal: |
  ${
    [1, 2, 3]
      .map(n => n * 2)
      .filter(n => n > 2)
  }
folded: >
  hello
  ${{name}}
kept: |
  line one ${{name}}
  line two
`)
		type shape struct {
			Literal []int  `json:"literal"`
			Folded  string `json:"folded"`
			Kept    string `json:"kept"`
		}
		got := evalAs[shape](t, template, map[string]any{"name": "ada"})
		expectEqual(t, got, shape{Literal: []int{4, 6}, Folded: "hello ada\n", Kept: "line one ada\nline two\n"})
	})
}

func TestExpressionsNestInListsAndMaps(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
servers:
  - name: ${names[0]}
    ports: [80, "${base + 1}"]
  - name: b-${{names[1]}}
    ports: ${[base + 2]}
`)
		type server struct {
			Name  string `json:"name"`
			Ports []int  `json:"ports"`
		}
		got := evalAs[[]server](t, template, map[string]any{"names": []string{"a", "b"}, "base": 8000}, "servers")
		expectEqual(t, got, []server{{Name: "a", Ports: []int{80, 8001}}, {Name: "b-b", Ports: []int{8002}}})
	})
}

func TestVarsAreDeepCopiesTheTemplateCannotMutate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: ${ (list.push(4), list.length) }\n")
		vars := map[string]any{"list": []int{1, 2, 3}}
		expectEqual(t, evalAs[int](t, template, vars, "a"), 4)
		expectEqual(t, vars["list"], []int{1, 2, 3})
	})
}

func TestVarsAreMarshalledAsJSON(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type user struct {
			Name   string `json:"name"`
			Secret string `json:"-"`
		}
		template := mustTemplate(t, sb, "a: ${ [u.name, u.Secret === undefined] }\n")
		got := evalAs[[]any](t, template, map[string]any{"u": user{Name: "ada", Secret: "x"}}, "a")
		expectEqual(t, got, []any{"ada", true})
	})
}

func TestExpressionsShareOneRuntimePerEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: ${ globalThis.counter = 1 }\nb: ${ typeof counter }\n")
		got := evalAs[map[string]any](t, template, nil)
		expectEqual(t, got, map[string]any{"a": float64(1), "b": "number"})
		again := evalAs[map[string]any](t, template, nil)
		expectEqual(t, again, map[string]any{"a": float64(1), "b": "number"})
	})
}

func TestExpressionsRunInStrictMode(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: ${ (function(){ return this === undefined })() }\nb: ${ (() => { undeclared = 1 })() }\n")
		expectEqual(t, evalAs[bool](t, template, nil, "a"), true)
		var out any
		expectError(t, template.Eval(t.Context(), &out, nil, "b"), EvaluationFailed, "ReferenceError")
	})
}

func TestInvalidExpressionsFailAtMakeInBothKinds(t *testing.T) {
	cases := map[string]struct {
		text      string
		fragments []string
	}{
		"syntax":            {"a: ${1 +}\n", []string{"$.a", "${1 +}", "syntax error"}},
		"interpolated":      {"a: \"x ${{ ) }}\"\n", []string{"$.a", "${{) }}", "syntax error at line 1, column 2", "Unexpected token )"}},
		"second line":       {"a: |\n  ${\n    1 +\n    +\n  }\n", []string{"syntax error"}},
		"unclosed whole":    {"a: ${1 + 1\n", []string{"unclosed ${"}},
		"unclosed double":   {"a: \"x ${{1\"\n", []string{"unclosed ${{"}},
		"double closes one": {"a: \"x ${{1}\"\n", []string{"unclosed ${{"}},
		"empty whole":       {"a: ${ }\n", []string{"empty expression"}},
		"empty double":      {"a: \"x ${{}}\"\n", []string{"empty expression"}},
		"not whole":         {"a: ${a} and ${b}\n", []string{"one whole expression"}},
		"statement":         {"a: ${ var x = 1 }\n", []string{"syntax error"}},
		"two statements":    {"a: ${ 1; 2 }\n", []string{"syntax error", "single JavaScript expression"}},
		"nested in list":    {"a:\n  - ok\n  - ${ ( }\n", []string{"$.a[1]"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			makeInBothKinds(t, c.text).expectRefused(t, InvalidTemplate, c.fragments...)
		})
	}
}

func TestSourceMapCommentsAreNotFollowed(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: |\n  ${ 1 //# sourceMappingURL=/etc/passwd\n  }\n")
		expectEqual(t, evalAs[int](t, template, nil, "a"), 1)
	})
}

func TestAnExpressionCannotEscapeItsContext(t *testing.T) {
	for _, source := range []string{"1)}); (function(){ return (2", "1)}), (function(){ return (2", "1), (2", "1; 2", "1\n2"} {
		if _, err := compileExpression(source, wholeValue); err == nil {
			t.Fatalf("%q: expected a refusal", source)
		}
	}
}

func TestStrictModeCompileErrorsFailAtMakeTemplate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeTemplate(t.Context(), "a: |\n  ${\n    delete x\n  }\n")
		expectError(t, err, InvalidTemplate, "$.a", "syntax error at line 2")
	})
}

func TestInputEndingInsideAJavaScriptTokenIsUnclosed(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: \"${ `x }\"\n", "a: \"${ `${ 1 }\"\n", "a: ${ /* x }\n", "a: ${ x // c }\n", "a: \"x ${{ `y }}\"\n"} {
			_, err := sb.MakeTemplate(t.Context(), text)
			expectError(t, err, InvalidTemplate, "unclosed")
		}
	})
}

func TestAnUnterminatedStringIsJavaScriptsOwnSyntaxError(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: ${ 'x }\n", "a: \"x ${{ 'abc }}\"\n"} {
			_, err := sb.MakeTemplate(t.Context(), text)
			expectError(t, err, InvalidTemplate, "syntax error at line 1, column 2", "Unexpected token ILLEGAL")
			expectNoFragment(t, err, "unclosed", "object literal")
		}
	})
}

func TestAnAliasCannotContainItself(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeTemplate(t.Context(), "a: &a [*a]\n")
		expectError(t, err, InvalidTemplate)
	})
}
