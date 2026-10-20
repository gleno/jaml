package jaml

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja/parser"
)

const (
	boundTestStack            = 16 << 20
	stackCappedEvaluatorLimit = 512<<20 + 13
)

var _ = capTheEvaluatorsStack()

func capTheEvaluatorsStack() bool {
	if os.Getenv(evaluatorMarker) != "1" || os.Getenv(evaluatorMemoryLimit) != strconv.Itoa(stackCappedEvaluatorLimit) {
		return false
	}
	debug.SetMaxStack(boundTestStack)
	return true
}

func parsedEnd(after string, opened _Delimiter) (int, error) {
	_, err := parser.ParseFile(nil, expressionFile, expressionOpens+after, 0, parser.WithDisableSourceMaps)
	var parseErrors parser.ErrorList
	if !errors.As(err, &parseErrors) || len(parseErrors) == 0 {
		return 0, errors.New("unclosed")
	}
	var offset = max(offsetOf(expressionOpens+after, parseErrors[0].Position)-len(expressionOpens), 0)
	switch {
	case strings.HasPrefix(after[offset:], opened.closes):
		return offset, nil
	case strings.HasPrefix(opened.closes, after[offset:]):
		return 0, errors.New("unclosed")
	}
	return 0, errors.New(parseErrors[0].Message)
}

func TestTheEndScanAgreesWithOneParseOfTheRest(t *testing.T) {
	var corpus = []string{
		" 1 }} tail }}", " '}}' }}", " {a: 1}}}", " `${ {b: '}}'}.b }` }} x }}", " /}}/.test(s) }} }}",
		" x // }}\n }} y }}", " /* }} */ 1 }}", " a }x}}", " 1}", " ) }}", " (1 }} }}", " '}}}' }} '}}' }}",
		" x => { return {a: '}}'} } }} }}", " [1, {b: 2}]}}}}", " `\\1` }} b }}", " 1 + `\\u` }} }}",
		" a / 2 }} x/ }}", " a.return / 2 }} x/ }}", " a.typeof / 2 }} x/ }}", " (a) / 2 }} x/ }}", " [1] / 2 }} x/ }}",
		" ({a: 1}).a / 2 }} x/ }}", " x => { if (a) /}}/.test(s); return 1 } }} }}", " x => { {} /}}/.test(s) } }}",
		" function () {} / 2 }} x/ }}", " typeof /}}/ }}", " a ? /}}/ : 1 }}", " a++ / 2 }} x/ }}", " `a${ '}}' }b` }}",
		" `${ `${ 1 }` }` }}", " /[/}}]/ }}", " a\n/ 2 }} x/ }}", " class { m() { return /}}/ } } }}", " class extends (a) { } }}",
		" 1.5.toFixed(1) }}", " .5 }}", " a?.5:1 }}", " a?.b }}", " x => ({ '}}': 1 }) }}", " '\\'}}' }}", " \"\\\"}}\" }}",
		" /\\/}}/ }}", " x => { while (a) /}}/; } }}", " x => { l: /}}/ } }}", " x => { a\n/}}/g } }}", " 0x1f / 2 }} x/ }}",
		" 1e+5 / 2 }} x/ }}", " x => { do /}}/; while (a) } }}", " this / 2 }} x/ }}", " a.if / 2 }} x/ }}", " ` }} ` }}",
		" (a, b) => a / b }} x/ }}", " [a] = [/}}/] }}", " a /= 2 }} x/ }}", " !/}}/.test(a) }}", " x => { if (a) {} /}}/; } }}",
		" x => { switch (a) { case 1: /}}/; default: } } }}", " a / 2 }} x/ }}", " 'é}}' + `é` }}", " a?.[0] / 2 }} x/ }}",
	}
	for _, after := range corpus {
		scanned, scannedErr := codeEnd(after, dollar.interpolation)
		parsed, parsedErr := parsedEnd(after, dollar.interpolation)
		if scannedErr != nil && parsedErr == nil {
			_, parsedErr = compileExpression(after[:parsed], dollar.interpolation)
			parsed = 0
		}
		if scanned != parsed || (scannedErr == nil) != (parsedErr == nil) {
			t.Errorf("%q: scanned %d %v, parsed %d %v", after, scanned, scannedErr, parsed, parsedErr)
		}
	}
}

type _BoundShape struct {
	name  string
	shape _CodeShape
	code  func(levels int) string
}

func labels(levels int) string {
	var written strings.Builder
	for level := range levels {
		fmt.Fprintf(&written, "l%d:", level)
	}
	return written.String()
}

var boundShapes = []_BoundShape{
	{"brackets", nestingShape, func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }},
	{"parentheses", nestingShape, func(n int) string { return strings.Repeat("(", n) + "0" + strings.Repeat(")", n) }},
	{"objects", nestingShape, func(n int) string { return strings.Repeat("{a:", n) + "0" + strings.Repeat("}", n) }},
	{"templates", nestingShape, func(n int) string { return strings.Repeat("`${", n) + "0" + strings.Repeat("}`", n) }},
	{"classes", nestingShape, func(n int) string { return strings.Repeat("class extends ", n) + "Object" + strings.Repeat("{}", n) }},
	{"arrows", arrowShape, func(n int) string { return strings.Repeat("x=>", n) + "0" }},
	{"parenthesised arrows", arrowShape, func(n int) string { return strings.Repeat("(x)=>", n) + "0" }},
	{"ifs", statementShape, func(n int) string { return "()=>{" + strings.Repeat("if(a)", n) + "0}" }},
	{"else ifs", statementShape, func(n int) string { return "()=>{" + strings.Repeat("if(a)0;else ", n) + "0}" }},
	{"labels", statementShape, func(n int) string { return "()=>{" + labels(n) + "0}" }},
	{"nots", prefixShape, func(n int) string { return strings.Repeat("!", n) + "0" }},
	{"negations", prefixShape, func(n int) string { return strings.Repeat("- ", n) + "0" }},
	{"typeofs", prefixShape, func(n int) string { return strings.Repeat("typeof ", n) + "0" }},
	{"assignments", rightShape, func(n int) string { return "(" + strings.Repeat("a=", n) + "0)" }},
	{"conditionals", rightShape, func(n int) string { return strings.Repeat("a?", n) + "0" + strings.Repeat(":0", n) }},
	{"alternates", rightShape, func(n int) string { return strings.Repeat("a?0:", n) + "0" }},
	{"powers", rightShape, func(n int) string { return strings.Repeat("1**", n) + "1" }},
	{"sums", binaryShape, func(n int) string { return strings.Repeat("1+", n) + "0" }},
	{"comparisons", binaryShape, func(n int) string { return strings.Repeat("a<", n) + "a" }},
	{"arrow parameter defaults", parameterShape, nested("(a=%s)=>0", "0")},
	{"arrow parameter patterns", parameterShape, nested("({a=[b=%s]})=>0", "0")},
	{"async arrow parameters", parameterShape, nested("async(a,b=%s)=>0", "0")},
	{"members", postfixShape, func(n int) string { return "a" + strings.Repeat(".a", n) }},
	{"calls", postfixShape, func(n int) string { return "f" + strings.Repeat("()", n) }},
	{"tags", postfixShape, func(n int) string { return "f" + strings.Repeat("``", n) }},
}

func TestEveryShapeBuildsAtItsBoundAndOneLevelMoreIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, shape := range boundShapes {
			var bound = shapeBounds[shape.shape]
			if _, err := sb.MakeExpression(t.Context(), shape.code(bound.limit)); err != nil {
				t.Errorf("%s at %d: %v", shape.name, bound.limit, err)
			}
			_, err := sb.MakeExpression(t.Context(), shape.code(bound.limit+1))
			expectError(t, err, InvalidExpression, "code too deep at line 1", fmt.Sprintf(bound.what, bound.limit))
		}
	})
}

func TestTheShapeBoundsHoldInValuesInterpolationsAndScripts(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var deep = strings.Repeat("x=>", 513) + "0"
		for _, text := range []string{"v: ${ " + deep + " }\n", "v: \"a ${{ " + deep + " }} b\"\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "$.v", "arrow functions nest more than 512 deep")
		}
		var document = mustJaml(t, sb, "v: ${ 1 }\n")
		var out any
		var err = resolveInto(WithScript(t.Context(), "const f = "+deep), &out, document.At("v"))
		expectError(t, err, InvalidExpression, "script 1", "arrow functions nest more than 512 deep")
		expectEqual(t, mustResolve[int](t, WithScript(t.Context(), "const a = 0, b = 1, c = 0, d = 1\nif (a) b; else if (c) d;\n"+strings.Repeat("if (a) b\n", 2000)), document.At("v")), 1)
	})
}

func saturatedShapes() string {
	return strings.Repeat("x=>", 511) + strings.Repeat("[", 510) + "()=>{" + labels(510) + "return " + strings.Repeat("!", 511) +
		"(" + strings.Repeat("a=", 511) + strings.Repeat("1+", 4095) + "a" + strings.Repeat(".a", 2047) + ")}" + strings.Repeat("]", 510)
}

func worstShapesUnderTheCap() map[string]string {
	var worst = map[string]string{"saturated": saturatedShapes()}
	for _, shape := range boundShapes {
		var levels = 1
		for len(shape.code(levels*2)) <= maxCodeBytes-len("${  }") {
			levels *= 2
		}
		for step := levels / 2; step > 0; step /= 2 {
			if len(shape.code(levels+step)) <= maxCodeBytes-len("${  }") {
				levels += step
			}
		}
		worst[shape.name] = shape.code(levels)
		worst[shape.name+" at the bound"] = shape.code(shapeBounds[shape.shape].limit)
	}
	return worst
}

func TestTheWorstShapesUnderTheCapBuildOrAreRefusedOnA16MiBStackInBothKinds(t *testing.T) {
	survivesInAChildWithMaxStack(t, boundTestStack, func(t *testing.T) {
		eachStackCappedKind(t, func(t *testing.T, sb *Sandbox) {
			for name, code := range worstShapesUnderTheCap() {
				for _, text := range []string{"v: ${ " + code + " }\n", "v: '" + "a ${{ " + strings.ReplaceAll(code, "'", "''") + " }}'\n"} {
					if _, err := sb.MakeExpressionFromJaml(t.Context(), text); err != nil && !errors.Is(err, InvalidExpression) {
						t.Errorf("%s: %v", name, err)
					}
				}
				if _, err := sb.MakeExpression(t.Context(), code); err != nil && !errors.Is(err, InvalidExpression) {
					t.Errorf("%s: %v", name, err)
				}
				var out any
				if err := resolveInto(WithScript(t.Context(), "const f = "+code), &out, mustJaml(t, sb, "v: 1\n")); err != nil && !errors.Is(err, InvalidExpression) && !errors.Is(err, EvaluationFailed) {
					t.Errorf("%s script: %v", name, err)
				}
			}
			if _, err := sb.MakeExpression(t.Context(), saturatedShapes()); err != nil {
				t.Fatalf("the saturated shape did not build: %v", err)
			}
		})
	})
}

func eachStackCappedKind(t *testing.T, body func(t *testing.T, sb *Sandbox)) {
	t.Helper()
	for _, kind := range sandboxKinds {
		var options = []SandboxOption{WithBuildTimeout(time.Minute)}
		if kind.name == isolatedKind.name {
			options = append(options, WithMemoryLimit(stackCappedEvaluatorLimit))
		}
		t.Run(kind.name, func(t *testing.T) {
			body(t, mustSandbox(t, kind, options...))
		})
	}
}

func TestAnArrowChainHidingManyClosersIsRefusedInOneScan(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var closers = strings.TrimSuffix(strings.Repeat("{},", 1024), ",")
		var started = time.Now()
		_, err := sb.MakeExpressionFromJaml(t.Context(), "a: \"${"+strings.Repeat("x=>", 19_500)+"["+closers+"]}\"\n")
		expectError(t, err, InvalidExpression, "$.a", "arrow functions nest more than 512 deep")
		mustJaml(t, sb, "a: \"${"+strings.Repeat("x=>", 512)+"["+closers+"]}\"\n")
		if elapsed := time.Since(started); elapsed > raceSlowdown*time.Second {
			t.Fatalf("refusing and building took %v", elapsed)
		}
	}, WithBuildTimeout(time.Minute))
}

func TestAStrictUndefinedArrowChainResolvesQuicklyAndHonorsItsContext(t *testing.T) {
	type _Verdict struct {
		OK bool `json:"ok"`
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: ${ ("+strings.Repeat("x=>", 512)+"0, undefined) }\nb: \"${ (d) => { ok: true } }\"\n")
		var out any
		var started = time.Now()
		for range 20 {
			expectError(t, resolveInto(t.Context(), &out, document.At("a")), CoercionFailed, "strict undefined")
		}
		if elapsed := time.Since(started); elapsed > raceSlowdown*time.Second {
			t.Fatalf("20 strict-undefined resolves took %v", elapsed)
		}
		expectError(t, resolveInto(cancelledContext(t), &out, document.At("a")), EvaluationFailed, "cancelled", "user hit stop")
		_, err := Resolve[_Verdict](t.Context(), document.At("b"), 1)
		expectError(t, err, CoercionFailed, objectLiteralHint)
	})
}

var regexPayloads = []string{"/}/", "/'/g", "/[/}]+/", "/\\/}/i", "/\"}\"/", "/\\d+/"}

var regexContexts = []string{
	"R.test('}')", "(R)", "[R]", "[1, R]", "f(R)", "f(1, R)", "a ? R : 1", "a ? 1 : R", "!R", "typeof R", "void R",
	"x => R", "async x => R", "(x) => R", "async (x) => R", "(x = R)", "a && R", "a || R", "a ?? R", "a + R", "a == R",
	"a !== R", "a in R", "({a: R})", "`${R}`", "`a${R}b${R}c`", "a[R]", "new RegExp(R)", "[...R]", "f(...R)",
	"(() => { return R })()", "(() => { if (a) R; })()", "(() => { if (a) {} R; })()", "(() => { while (a) R; })()",
	"(() => { for (const m of R.exec('}')) return m })()", "(() => { for (const [m] of R) ; })()", "(() => { for (m of R) ; })()",
	"(() => { for (const {m} of R) ; })()", "(() => { for (let i = 0; i < R; i++) ; })()", "(() => { for (const k in R) ; })()",
	"(async () => { await R })()", "(async () => { for await (const m of R) ; })()", "(async function () { return await R })()",
	"async () => await R", "(async x => await R)", "(function* () { yield R })()", "(function* () { yield* R })()",
	"(() => { switch (a) { case R: break; default: R } })()", "(() => { do R; while (a) })()", "(() => { throw R })()",
	"(() => { l: R })()", "(() => { a;\nR })()", "(() => { {} R })()", "(() => { function f() {} R })()",
	"(() => { class A {} R })()", "(() => { const f = () => {}\nR })()", "(() => { try {} catch (e) {} R })()",
	"(() => { try {} finally {} R })()", "(() => { if (a) {} else R })()", "(() => { if (a) 1; else R })()",
	"(() => { return\nR })()", "({ m() { return R } })", "({ get x() { return R } })", "({ async m() { await R } })",
	"({ *g() { yield R } })", "({ async *g() { yield await R } })", "(class { m() { return R } static s = R })",
	"(class { static { R } })", "(class extends Object { m() { return super.m(R) } })", "a?.[R]", "a?.(R)",
	"tag`${R}`", "(() => { const o = { of: R }; return o.of })()", "(() => { let x = 1; x\n++x; return R })()",
	"(() => { var await = 1; return [await, R] })()", "(async () => { const x = () => R; await x })()",
	"(() => { async function g() { await R } return g })()", "(() => { label: for (;;) { break label } R })()",
	"(() => { if (a) for (;;) R; })()", "(() => { async () => {}\nR })()",
}

var divisionContexts = []string{
	"a / 2 / b", "a.of / 2 / 1", "a.await / 2 / 1", "a.yield / 2 / 1", "a.return / 2 / 1", "(a) / 2 / 1", "[a] / 2 / 1",
	"a[0] / 2 / 1", "f() / 2 / 1", "1 / 2 / 1", "'s' / 2 / 1", "`t` / 2 / 1", "x++ / 2 / 1", "this / 2 / 1",
	"null / 2 / 1", "true / 2 / 1", "(function () {}) / 2 / 1", "function () {} / 2 / 1", "({}) / 2 / 1",
	"(() => { var await = 6; return await / 2 / 1 })()", "(() => { var of = 6; return of / 2 / 1 })()",
	"(() => { var async = 6; return async / 2 / 1 })()", "(() => { let of = [4]; for (const x of of) return x / 2 / 1 })()",
	"(function () { var await = 4; return await / 2 / 1 })()", "(async () => (function () { var await = 1; return await / 1 / 1 })())()",
	"(() => { var get = 2, set = 4, of = 6; return get / set / of })()", "a\n/ 2 / 1", "a /= 2", "a.b.c / 2 / 1",
	"(class {}) / 2 / 1", "0x10 / 2 / 1", "1e3 / 2 / 1", ".5 / 2 / 1", "a?.b / 2 / 1", "a.#b", "(x => x) / 2 / 1",
	"(async () => { var f = function () { var await = 2; return await / 2 / 1 }; return f() })()",
	"(async () => { function g() { var await = 1; return await / 2 / 1 } return g })()", "(() => { var async = 1; return async / 2 / 1 })()",
	"(async () => { const o = { await: 1 }; return o.await / 2 / 1 })()", "(() => { var of = 2; for (const x of [of / 2 / 1]) ; return of / 2 / 1 })()",
	"(() => { var await = 2; const f = x => await / 2 / 1; return f })()", "(() => { var await = 2; return { m() { return await / 2 / 1 } } })()",
	"(async function () { return class { m() { var await = 1; return await / 2 / 1 } } })()",
}

var templateCorpus = []string{
	"`${ `${ '}' }` }`", "`a${ {b: '}'}.b }c`", "`${ (() => { return `}` })() }`", "`\\${ }`", "`$`", "`${1}${2}`",
	"tag`x${ '}' }y`", "`${ /}/.source }`", "`${ `${ `${ '}}' }` }` }`", "`}` + '}' + \"}\"", "`${ {} }`",
	"`${ x => { return { a: '}' } } }`", "`\n}\n`", "`${'`'}`",
}

var scriptCorpus = []string{
	"let a = 1\nif (a) a++\nelse a--\n", "function f() { return 1 }\n/}/.test('}')\n", "class A {}\n/}/.test('}')\n",
	"const f = () => {}\n/}/.test('}')\n", "for (const m of /\\d+/.exec('12')) m\n", "var await = 6\nconst x = await / 2 / 1\n",
	"async function g() { for await (const x of y) {} return await /}/ }\n", "function* g() { yield /}/; yield* [] }\n",
	"label: for (;;) { break label }\n", "a: b: c: 1\n", "switch (1) { case /}/.source: break; default: }\n",
	"try { throw /}/ } catch (e) { e } finally { /}/ }\n", "if (1) { } /}/.test('}')\n", "do x++; while (0)\n",
	"x = 1\n++x\n", "x = y\n(1)\n", "const o = { get a() { return /}/ }, set a(v) {}, async b() { await /}/ }, *c() { yield /}/ } }\n",
	"class B { static s = /}/; #p = /}/; m() { return this.#p } static { /}/ } }\n", "x = a\n? b : c\n",
	"const t = `${ `}` }`\n", "let y = 1;; /}/.test('}')\n", "if (a) /}/.test(a); else /}/.test(b)\n", "while (0) /}/\n",
	"for (;;) break\n/}/\n", "{ /}/ }\n", "a\n++b\n", "async () => {}\n/}/.test('}')\n", "var of = 1\nof / 2 / 1\n",
	"async function h() { const r = await /}/; return r }\nh\n", "function k() {}\nk()\n/}/\n", "for (const x of [1]) { }\n/}/\n",
	"const v = 1\n/* } */\n// }\nv\n", "x = { a: 1 }\n/}/.test('}')\n", "if (a) {\n} else {\n}\n/}/\n",
	"function* q() { const x = yield; yield /}/ }\n", "export_ = 1\n", "let z = a\n/ 2 / 1\n", "x\n++\ny\n",
	"class C extends Object { constructor() { super(); this.r = /}/ } }\n", "async function* w() { for await (const v of x) yield /}/ }\n",
}

func differentialExpressions() []string {
	var corpus []string
	for _, context := range regexContexts {
		for _, payload := range regexPayloads {
			corpus = append(corpus, strings.ReplaceAll(context, "R", payload))
		}
	}
	for _, context := range divisionContexts {
		corpus = append(corpus, context, strings.ReplaceAll(context, " / 2 / 1", " / '}'.length"))
	}
	return append(corpus, templateCorpus...)
}

func TestTheScannerNeverRefusesWhatGojaAccepts(t *testing.T) {
	var accepted int
	for _, expression := range differentialExpressions() {
		if _, err := parseExpression(expression); err != nil {
			continue
		}
		accepted++
		if err := refuseDeepExpression(expression); err != nil {
			t.Errorf("%q: goja parses it, the scanner refuses: %v", expression, err)
		}
		var code = " " + expression + " "
		if end, err := codeEnd(code+"}} '}}' }}", dollar.interpolation); err != nil || end != len(code) {
			t.Errorf("%q: interpolation ends at %d %v, not %d", expression, end, err, len(code))
		}
		if end, err := codeEnd(code+"}", dollar.value); err != nil || end != len(code) {
			t.Errorf("%q: value ends at %d %v, not %d", expression, end, err, len(code))
		}
	}
	for _, script := range scriptCorpus {
		if _, err := parser.ParseFile(nil, "", scriptOpens+script, 0, parser.WithDisableSourceMaps); err != nil {
			continue
		}
		accepted++
		if err := refuseDeepScript(script); err != nil {
			t.Errorf("script %q: goja parses it, the scanner refuses: %v", script, err)
		}
	}
	if accepted < 600 {
		t.Fatalf("only %d corpus entries parse in goja", accepted)
	}
}

func TestCodeThatBuiltBeforeTheScannerStillBuilds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		expectEqual(t, mustResolve[[]string](t, t.Context(), mustExpression(t, sb, `(() => { const out = []; for (const m of /\d+/.exec('12')) out.push(m); return out })()`)), []string{"12"})
		expectEqual(t, mustResolve[string](t, t.Context(), mustExpression(t, sb, `(() => { for (const m of /'/g.exec("'")) return m })()`)), "'")
		expectEqual(t, mustResolve[map[string]string](t, t.Context(), mustJaml(t, sb, "v: ${ (() => { for (const m of /x}/.exec('x}')) return m })() }\n")), map[string]string{"v": "x}"})
		expectEqual(t, mustResolve[int](t, t.Context(), mustExpression(t, sb, "(() => { var await = 6; return await / 2 })()")), 3)
		var document = mustJaml(t, sb, "v: ${ half }\n")
		expectEqual(t, mustResolve[int](t, WithScript(t.Context(), "var await = 6\nconst half = await / 2\n"), document.At("v")), 3)
	})
}

func siblings(count int, sibling func(index int) string, separator string) string {
	var written []string
	for index := range count {
		written = append(written, sibling(index))
	}
	return strings.Join(written, separator)
}

func manySiblingExpressions() map[string]string {
	var many = 2 * shapeBounds[binaryShape].limit
	var fields = 2*shapeBounds[rightShape].limit + 100
	var field = func(index int) string { return fmt.Sprintf("f%d = a ? %d : () => b + c", index, index) }
	return map[string]string{
		"class fields on lines":         "(class { " + siblings(fields, field, "\n") + " })",
		"class fields with semicolons":  "(class { " + siblings(fields, field, "; ") + " })",
		"static class fields on lines":  "(class { " + siblings(fields, func(i int) string { return fmt.Sprintf("static s%d = !a", i) }, "\n") + " })",
		"class arrow fields on lines":   "(class { " + siblings(fields, func(i int) string { return fmt.Sprintf("f%d = () => {}", i) }, "\n") + " })",
		"private class fields on lines": "(class { " + siblings(fields, func(i int) string { return fmt.Sprintf("#p%d = a ** 2", i) }, "\n") + " })",
		"class methods":                 "(class { " + siblings(fields, func(i int) string { return fmt.Sprintf("m%d(x = a ? 1 : 2) { return x }", i) }, "\n") + " })",
		"class accessors": "(class { " + siblings(fields, func(i int) string {
			return fmt.Sprintf("get g%d() { return a ? 1 : 2 } set g%d(v) { this.v = v }", i, i)
		}, "\n") + " })",
		"object entries":                 "({ " + siblings(many, func(i int) string { return fmt.Sprintf("k%d: a ? x => x : -b + c.d", i) }, ", ") + " })",
		"object accessors":               "({ " + siblings(fields, func(i int) string { return fmt.Sprintf("get g%d() { return a = 1 }, set g%d(v) { a = v }", i, i) }, ", ") + " })",
		"destructuring defaults":         "(({ " + siblings(many, func(i int) string { return fmt.Sprintf("d%d = a ? 1 : 2", i) }, ", ") + " }) => 0)",
		"array of conditionals":          "[" + siblings(many, func(int) string { return "a ? b : c" }, ", ") + "]",
		"arguments":                      "f(" + siblings(many, func(int) string { return "a = b ? !c : d.e.f" }, ", ") + ")",
		"parameters":                     "((" + siblings(many, func(i int) string { return fmt.Sprintf("p%d = x => x", i) }, ", ") + ") => 0)",
		"sequence":                       "(" + siblings(many, func(int) string { return "a = b ** c + d" }, ", ") + ")",
		"template substitutions":         "`" + siblings(many, func(int) string { return "${a ? b : c}" }, "") + "`",
		"cases":                          "(() => { switch (x) { " + siblings(fields, func(i int) string { return fmt.Sprintf("case %d: y = a ? 1 : x => !x", i) }, "\n") + " } })",
		"cases with breaks":              "(() => { switch (x) { " + siblings(fields, func(i int) string { return fmt.Sprintf("case %d: if (a) y = b; break", i) }, "\n") + "\ndefault: } })",
		"statements on lines":            "(() => { " + siblings(many, func(int) string { return "x = a ? b + c : () => {}" }, "\n") + " })",
		"arrow statements on lines":      "(() => { " + siblings(fields, func(i int) string { return fmt.Sprintf("const f%d = () => {}", i) }, "\n") + " })",
		"ifs on lines":                   "(() => { " + siblings(many, func(int) string { return "if (a) x = !b" }, "\n") + " })",
		"labels on lines":                "(() => { " + siblings(fields, func(i int) string { return fmt.Sprintf("l%d: x = a.b.c", i) }, "\n") + " })",
		"increments on lines":            "(() => { let x = 0\n" + siblings(many, func(int) string { return "x\n++x" }, "\n") + " })",
		"binary statements on lines":     "(() => { " + siblings(many, func(int) string { return "a + b * c" }, "\n") + " })",
		"member statements on lines":     "(() => { " + siblings(many, func(int) string { return "a.b.c()" }, "\n") + " })",
		"function declarations on lines": "(() => { " + siblings(fields, func(i int) string { return fmt.Sprintf("function f%d() { return a ? 1 : 2 }", i) }, "\n") + " })",
	}
}

func TestSiblingsNeverChainIntoOneShape(t *testing.T) {
	for name, expression := range manySiblingExpressions() {
		if _, err := parseExpression(expression); err != nil {
			t.Errorf("%s: goja refuses the test input: %v", name, err)
			continue
		}
		if err := refuseDeepExpression(expression); err != nil {
			t.Errorf("%s: goja parses it, the scanner refuses: %v", name, err)
		}
		if !strings.HasPrefix(expression, "(() => { ") {
			continue
		}
		var body = strings.TrimSuffix(strings.TrimPrefix(expression, "(() => { "), " })")
		if err := refuseDeepScript(body); err != nil {
			t.Errorf("%s as a script: the scanner refuses: %v", name, err)
		}
	}
}
