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
