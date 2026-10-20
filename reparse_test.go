package jaml

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

const (
	linearGrowth       = 2.5
	compositions       = 1000
	compositionLevels  = 24
	compositionBytes   = 16 << 10
	compileAllocations = 48
)

type _Wrapper string

func (w _Wrapper) around(inner string) string {
	return strings.ReplaceAll(string(w), "%s", inner)
}

func nested(wrapper _Wrapper, innermost string) func(levels int) string {
	return func(levels int) string {
		var code = innermost
		for range levels {
			code = wrapper.around(code)
		}
		return code
	}
}

var reparsedHeads = []_Wrapper{
	"(a=%s)=>0", "([a=%s])=>0", "({a=%s})=>0", "async(a=%s)=>0", "(...[%s])=>0", "(b,a=%s)=>0", "async(b,%s)=>0",
	"(a=[%s])=>0", "(a={b:%s})=>0", "(x=(a=>%s))=>0", "(a=async(%s))=>0", "(a=`${%s}`)=>0", "(a=class{m(){return %s}})=>0",
	"(a=(%s))=>0",
}

var onceParsed = []_Wrapper{
	"[%s]", "{a:%s}", "(%s)", "`${%s}`", "f(%s)", "async(%s)", "x=>%s", "(x)=>{return %s}", "a?%s:0", "[a=%s]=0",
	"({a:b=%s}=0)", "(class{m(a=%s){}})", "(function(a=%s){})", "(function*(){yield %s})", "(()=>{for(const [a=%s] of x);})",
	"!%s", "a.b[%s]", "(x,(y,%s))", "((a))=%s", "(x)=>(y)=>%s",
}

func compileWork(code string) float64 {
	return testing.AllocsPerRun(1, func() { _, _ = compileExpression(code, dollar.value) })
}

func TestEveryCoverGrammarNestingIsRefusedOrCompilesInLinearWork(t *testing.T) {
	var depth = 2 * shapeBounds[parameterShape].limit
	for _, wrapper := range append(append([]_Wrapper{}, reparsedHeads...), onceParsed...) {
		var code = nested(wrapper, "0")
		if refuseDeepExpression(code(2*depth)) != nil {
			continue
		}
		var growth = compileWork(code(2*depth)) / compileWork(code(depth))
		if growth > linearGrowth {
			t.Errorf("%s: doubling its nesting from %d multiplies compile work by %.1f", wrapper, depth, growth)
		}
	}
	for _, wrapper := range reparsedHeads {
		var code = nested(wrapper, "0")
		if err := refuseDeepExpression(code(shapeBounds[parameterShape].limit)); err != nil {
			t.Errorf("%s at the bound: %v", wrapper, err)
		}
		if err := refuseDeepExpression(code(shapeBounds[parameterShape].limit + 1)); err == nil || !strings.Contains(err.Error(), "arrow parameter lists nest") {
			t.Errorf("%s past the bound: %v", wrapper, err)
		}
	}
}

func TestNoCompositionOfCoverGrammarsCompilesInSuperlinearWork(t *testing.T) {
	var random = rand.New(rand.NewPCG(20261020, 1))
	var wrappers = append(append([]_Wrapper{"%s,%s"}, reparsedHeads...), onceParsed...)
	for composition := range compositions {
		var code = "0"
		var applied []string
		for range compositionLevels {
			var wrapper = wrappers[random.IntN(len(wrappers))]
			var wrapped = wrapper.around(code)
			if len(wrapped) > compositionBytes {
				break
			}
			code = wrapped
			applied = append(applied, string(wrapper))
			if refuseDeepExpression(code) != nil {
				break
			}
			if work := compileWork(code); work > compileAllocations*float64(len(code))+1000 {
				t.Fatalf("composition %d (%s) of %d bytes compiles in %.0f allocations", composition, strings.Join(applied, " ∘ "), len(code), work)
			}
		}
	}
}

func TestReparsedHeadsAreRefusedInEveryPathThatCompiles(t *testing.T) {
	var bound = shapeBounds[parameterShape]
	var deep = nested("(a=%s)=>0", "0")(bound.limit + 1)
	var message = fmt.Sprintf(bound.what, bound.limit)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpression(t.Context(), deep)
		expectError(t, err, InvalidExpression, message)
		for _, text := range []string{"v: ${ " + deep + " }\n", "v: \"a ${{ " + deep + " }} b\"\n"} {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, "$.v", message)
		}
		var out any
		err = resolveInto(WithScript(t.Context(), "const f = "+deep), &out, mustJaml(t, sb, "v: 1\n"))
		expectError(t, err, InvalidExpression, "script 1", message)
	})
}
