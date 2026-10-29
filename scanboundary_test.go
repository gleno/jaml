package jaml

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

var keywordMemberExpressions = []string{
	`({class: 1, async m() { return await /}/.test('}') }})`,
	`({async() { var await=6; return await / 2 }})`,
	`({get async() { var await=6; return await / 2 }})`,
	`({async await() { return await /}/.test('}') }})`,
	`({async yield() { return await /}/.test('}') }})`,
	`({async function() { return await /}/.test('}') }})`,
	`({async return() { return await /}/.test('}') }})`,
	`({async if() { return await /}/.test('}') }})`,
	`({async 'm'() { return await /}/.test('}') }})`,
	`({async ['m']() { return await /}/.test('}') }})`,
	`(class { async() { var await=6; return await / 2 } })`,
	`(class { class=1; async m() { return await /}/.test('}') } })`,
	`(class { async await() { return await /}/.test('}') } })`,
	`(async () => ({if() { var await=6; return await / 2 }}))`,
	`(async () => ({while() { var await=6; return await / 2 }}))`,
	`(async () => ({switch() { var await=6; return await / 2 }}))`,
	`({async /* modifier */ m() { return await /}/ }, async /* name */ () { var await=6; return await / 2 }})`,
	`({get /* accessor */ async() { var await=6; return await / 2 }, *async() { yield /}/ }})`,
	`({*['m']() { yield /}/ }})`,
	`({value: class extends Object { async m() { return await /}/ } }})`,
	`(class { static async() { var await=6; return await / 2 } static async m() { return await /}/ } })`,
	"(class { async\nm() { var await=6; return await / 2 } })",
	"(class { async /* line\n */ m() { return await /}/ } })",
	"(class { async \n/* line */ m() { var await=6; return await / 2 } })",
	`(async () => ({ m(x = await / 2) { return x } }))`,
	`(async () => ({ async(x = await / 2) { return x } }))`,
	`(async () => ({ ['m'](x = await / 2) { return x } }))`,
	"(async () => class { field\n['m'](x = await / 2) { return x } })",
	"(class { field\n*method(x = await / 2) { yield x } })",
	"(class { field = [1]\n[0] })",
	"(class { field = 1\nin {} })",
	"(class { field = []\ninstanceof Array })",
}

func expectNativeScannedExpression(t *testing.T, source string) {
	t.Helper()
	parsed, err := parseExpression(source)
	if err != nil {
		t.Fatalf("native parser rejected fixture %q: %v", source, err)
	}
	if _, err := goja.CompileAST(parsed, true); err != nil {
		t.Fatalf("native compiler rejected fixture %q: %v", source, err)
	}
	if err := refuseDeepExpression(source); err != nil {
		t.Errorf("scanner rejected native expression %q: %v", source, err)
	}
	for _, opened := range []_Delimiter{dollar.value, dollar.interpolation} {
		var code = " " + source + " "
		if end, err := codeEnd(code+opened.closes+" tail }", opened); err != nil || end != len(code) {
			t.Errorf("%s scan of %q ended at %d, %v; want %d", opened.opens, source, end, err, len(code))
		}
	}
}

func TestKeywordMemberNamesPreserveTheirLexicalRole(t *testing.T) {
	for _, source := range keywordMemberExpressions {
		expectNativeScannedExpression(t, source)
	}
	for _, name := range strings.Fields("async await break case catch class const continue debugger default delete do else enum export extends false finally for function get if implements import in instanceof interface let new null of package private protected public return set static super switch this throw true try typeof var void while with yield") {
		t.Run(name, func(t *testing.T) {
			expectNativeScannedExpression(t, "(async () => class { field\n"+name+"(x = await / 2) { return x } })")
		})
	}
	expectNativeScannedExpression(t, "({"+strings.Repeat("class:0,", 600)+"})")
}

func TestUnicodeWhitespaceEndsNumbersAndRegexpFlags(t *testing.T) {
	for name, separator := range map[string]string{"NBSP": "\u00a0", "BOM": "\ufeff", "line": "\u2028", "paragraph": "\u2029"} {
		t.Run(name, func(t *testing.T) {
			for _, operand := range []string{"1", "0x1", "1e0", "/x/", "/x/g"} {
				expectNativeScannedExpression(t, operand+separator+"in /}/")
			}
			if name == "line" || name == "paragraph" {
				expectNativeScannedExpression(t, "(() => { 1"+separator+"return /}/.source })()")
			}
		})
	}
}

func TestUnicodeWhitespaceDoesNotHideOperatorDepth(t *testing.T) {
	var bound = shapeBounds[binaryShape]
	for _, separator := range []string{"\u00a0", "\ufeff", "\u2028", "\u2029"} {
		for _, source := range []string{
			strings.Repeat("1"+separator+"in ", bound.limit+1) + "{}",
			"() => { " + strings.Repeat("1\nin"+separator, bound.limit+1) + "{} }",
		} {
			if err := refuseDeepExpression(source); err == nil || !strings.Contains(err.Error(), "binary operators chain") {
				t.Errorf("Unicode-separated binary chain escaped its bound: %v", err)
			}
		}
	}
}

func TestLexicalBoundariesWorkThroughEveryCompiler(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, source := range []string{
			`({async() { var await=6; return await / 2 }}).async()`,
			`({class: 1, async m() { return await /}/.test('}') }}).class`,
			"1\u00a0in /}/",
			"/x/\ufeffin /}/",
			"(() => { 1\u2028return /}/.source })()",
		} {
			t.Run(source, func(t *testing.T) {
				var plain = mustExpression(t, sb, source)
				var want = mustResolve[any](t, t.Context(), plain)
				var whole = mustJaml(t, sb, "v: "+quotedYAML("${ "+source+" }")+"\n")
				expectEqual(t, mustResolve[any](t, t.Context(), whole.At("v")), want)
				var interpolation = mustJaml(t, sb, "v: "+quotedYAML("x ${{ "+source+" }} y")+"\n")
				var interpolated = mustResolve[string](t, t.Context(), interpolation.At("v"))
				expectEqual(t, interpolated, "x "+fmt.Sprint(want)+" y")
				var scripted = mustExpression(t, sb, "value")
				var script = "const value = " + source + ";\n"
				if _, err := parser.ParseFile(nil, "", scriptOpens+script, 0, parser.WithDisableSourceMaps); err != nil {
					t.Fatalf("native script parser rejected fixture: %v", err)
				}
				expectEqual(t, mustResolve[any](t, WithScript(t.Context(), script), scripted), want)
			})
		}
	})
}

func quotedYAML(text string) string {
	return strconv.Quote(text)
}
