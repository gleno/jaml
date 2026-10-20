package jaml

import "testing"

func TestAWholeExpressionKeepsItsType(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
number: ${1+1}
fraction: ${1/4}
text: ${'a' + 'b'}
truth: ${1 < 2}
nothing: ${null}
list: ${[1, 'two', true]}
object: "${ {a: 1, b: [2]} }"
`)
		var got = evalAs[map[string]any](t, parsed, nil)
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
		var parsed = mustJaml(t, sb, "plain: ${1+1}\ndouble: \"${1+1}\"\nsingle: '${1+1}'\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"plain": float64(2), "double": float64(2), "single": float64(2)})
	})
}

func TestWhitespaceAroundAWholeExpressionIsIgnored(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: \"  ${ 1 + 1 }  \"\n")
		expectEqual(t, mustResolve[int](t, t.Context(), parsed.At("a")), 2)
	})
}

func TestInterpolationAlwaysMakesText(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
alone: ${{1+1}}
mixed: "n=${{n}}, twice=${{n*2}}!"
list: "items: ${{[1, 'a', null]}}"
object: "o: ${{ {k: 'v'} }}"
truth: "${{true}} and ${{1 > 2}}"
`)
		var got = evalAs[map[string]any](t, parsed, map[string]any{"n": 21})
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
		var parsed = mustJaml(t, sb, "a: \"  x ${{1}}  \"\n")
		expectEqual(t, mustResolve[string](t, t.Context(), parsed.At("a")), "  x 1  ")
	})
}

func TestNumbersInterpolateAsJavaScriptWritesThem(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: \"${{1e21}} ${{0.1+0.2}} ${{-0}} ${{2**53}} ${{1e-7}}\"\n")
		expectEqual(t, mustResolve[string](t, t.Context(), parsed.At("a")), "1e+21 0.30000000000000004 0 9007199254740992 1e-7")
	})
}

func TestBracesInsideStringsTemplatesAndCommentsDoNotCloseTheExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
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
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"quoted": "}}", "templated": "a}c", "interpolated": "x}}y", "commented": float64(2)})
	})
}

func TestAJavaScriptStringWritesLiteralDelimiters(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: \"${{'${{'}}not code}}\"\nb: ${'${x}'}\n")
		var got = evalAs[map[string]string](t, parsed, nil)
		expectEqual(t, got, map[string]string{"a": "${{not code}}", "b": "${x}"})
	})
}

func TestSingleBracesInsideTextAreLiteral(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: echo ${HOME}/bin\n")
		expectEqual(t, mustResolve[string](t, t.Context(), parsed.At("a")), "echo ${HOME}/bin")
	})
}

func TestKeysAreNeverEvaluated(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "${1+1}: a\n\"${{x}}\": b\n")
		var got = evalAs[map[string]string](t, parsed, nil)
		expectEqual(t, got, map[string]string{"${1+1}": "a", "${{x}}": "b"})
	})
}

func TestBlockScalarsHoldExpressions(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
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
		type _Decoded struct {
			Literal []int  `json:"literal"`
			Folded  string `json:"folded"`
			Kept    string `json:"kept"`
		}
		var got = mustResolve[_Decoded](t, valued(t.Context(), map[string]any{"name": "ada"}), parsed)
		expectEqual(t, got, _Decoded{Literal: []int{4, 6}, Folded: "hello ada\n", Kept: "line one ada\nline two\n"})
	})
}

func TestExpressionsNestInListsAndMaps(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, `
servers:
  - name: ${names[0]}
    ports: [80, "${base + 1}"]
  - name: b-${{names[1]}}
    ports: ${[base + 2]}
`)
		type _Server struct {
			Name  string `json:"name"`
			Ports []int  `json:"ports"`
		}
		var got = evalAs[[]_Server](t, parsed, map[string]any{"names": []string{"a", "b"}, "base": 8000}, "servers")
		expectEqual(t, got, []_Server{{Name: "a", Ports: []int{80, 8001}}, {Name: "b-b", Ports: []int{8002}}})
	})
}

func TestValuesAreFrozenCopiesAnExpressionCannotMutate(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "copied: ${ [...list, 4].length }\npushed: ${ (list.push(4), list.length) }\nsorted: ${ list.toSorted((a, b) => b - a) }\n")
		var values = map[string]any{"list": []int{1, 2, 3}}
		var ctx = valued(t.Context(), values)
		expectEqual(t, mustResolve[int](t, ctx, document.At("copied")), 4)
		expectEqual(t, mustResolve[[]int](t, ctx, document.At("sorted")), []int{3, 2, 1})
		_, err := Resolve[int](ctx, document.At("pushed"))
		expectError(t, err, EvaluationFailed, "$.pushed", "TypeError: cannot change a read-only value")
		expectEqual(t, values["list"], []int{1, 2, 3})
	})
}

func TestVarsAreMarshalledAsJSON(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type _User struct {
			Name   string `json:"name"`
			Secret string `json:"-"`
		}
		var parsed = mustJaml(t, sb, "a: ${ [u.name, u.Secret === undefined] }\n")
		var got = evalAs[[]any](t, parsed, map[string]any{"u": _User{Name: "ada", Secret: "x"}}, "a")
		expectEqual(t, got, []any{"ada", true})
	})
}

func TestExpressionsShareOneRuntimePerEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: ${ globalThis.counter = 1 }\nb: ${ typeof counter }\n")
		var got = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, got, map[string]any{"a": float64(1), "b": "number"})
		var again = evalAs[map[string]any](t, parsed, nil)
		expectEqual(t, again, map[string]any{"a": float64(1), "b": "number"})
	})
}

func TestExpressionsRunInStrictMode(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: ${ (function(){ return this === undefined })() }\nb: ${ (() => { undeclared = 1 })() }\n")
		expectEqual(t, mustResolve[bool](t, t.Context(), parsed.At("a")), true)
		var out any
		expectError(t, resolveInto(t.Context(), &out, parsed.At("b")), EvaluationFailed, "ReferenceError")
	})
}

func TestInvalidExpressionsFailAtMakeInBothKinds(t *testing.T) {
	var cases = map[string]struct {
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
			makeInBothKinds(t, c.text).expectRefused(t, InvalidExpression, c.fragments...)
		})
	}
}

func TestSourceMapCommentsAreNotFollowed(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var parsed = mustJaml(t, sb, "a: |\n  ${ 1 //# sourceMappingURL=/etc/passwd\n  }\n")
		expectEqual(t, mustResolve[int](t, t.Context(), parsed.At("a")), 1)
	})
}

func TestAnExpressionCannotEscapeItsContext(t *testing.T) {
	for _, source := range []string{"1)}); (function(){ return (2", "1)}), (function(){ return (2", "1), (2", "1; 2", "1\n2"} {
		if _, err := compileExpression(source, dollar.value); err == nil {
			t.Fatalf("%q: expected a refusal", source)
		}
	}
}

func TestStrictModeCompileErrorsFailAtMakeTemplate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: |\n  ${\n    delete x\n  }\n")
		expectError(t, err, InvalidExpression, "$.a", "syntax error at line 2")
	})
}

func TestInputEndingInsideAJavaScriptTokenIsUnclosed(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: \"${ `x }\"\n", "a: \"${ `${ 1 }\"\n", "a: ${ /* x }\n", "a: ${ x // c }\n", "a: \"x ${{ `y }}\"\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "unclosed")
		}
	})
}

func TestAnUnterminatedStringIsJavaScriptsOwnSyntaxError(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, text := range []string{"a: ${ 'x }\n", "a: \"x ${{ 'abc }}\"\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "syntax error at line 1, column 2", "Unexpected token ILLEGAL")
			expectNoFragment(t, err, "unclosed", "object literal")
		}
	})
}

func TestAnAliasCannotContainItself(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: &a [*a]\n")
		expectError(t, err, InvalidExpression)
	})
}
