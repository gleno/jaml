package jaml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func expectWellFormedAndBounded(t *testing.T, err error) {
	t.Helper()
	text := err.Error()
	if !utf8.ValidString(text) {
		t.Fatalf("invalid UTF-8 in %q", text)
	}
	if strings.ContainsAny(text, "\n\r\t") {
		t.Fatalf("raw whitespace in %q", text)
	}
}

func TestAPreviewCutsBetweenCharacters(t *testing.T) {
	long := strings.Repeat("й", 70)
	both := evalInBothKinds(t, "v: \"${ '"+long+"' + q }\"\n", nil)
	both.expectRefused(t, EvaluationFailed, "$.v: ${'"+strings.Repeat("й", 59)+"…}: ReferenceError: q is not defined")
	expectWellFormedAndBounded(t, both.trusted)
	made := makeInBothKinds(t, "v: \"${ '"+long+"' + ) }\"\n")
	made.expectRefused(t, InvalidTemplate, "…")
	expectWellFormedAndBounded(t, made.trusted)
}

func TestSyntaxErrorsCountCharactersAndQuoteWhereTheyPoint(t *testing.T) {
	cases := map[string]struct {
		text, expected string
	}{
		"accented":  {"v: \"${ \\\"ééééé\\\" + ) }\"\n", `syntax error at line 1, column 12 of the expression: Unexpected token ) at ") }"`},
		"leading":   {"v: \"${   'ééé' + 1 +* 2 }\"\n", `syntax error at line 1, column 15 of the expression: Unexpected token * at "* 2 }"`},
		"multiline": {"v: |\n  ${ 1 +\n    é * * 2 }\n", `syntax error at line 2, column 7 of the expression: Unexpected token * at "* 2 }"`},
		"octal":     {"v: \"${ 'é\\\\07' }\"\n", `syntax error at line 1, column 2 of the expression: Octal escape sequences are not allowed in strict mode at "'é\\07'"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			makeInBothKinds(t, c.text).expectRefused(t, InvalidTemplate, c.expected)
		})
	}
}

func TestThrownValuesAreNamedByTheirType(t *testing.T) {
	cases := map[string]struct {
		source, expected string
	}{
		"symbol":             {"throw Symbol('s')", ": threw Symbol(s): "},
		"anonymous symbol":   {"throw Symbol()", ": threw Symbol(): "},
		"symbol message":     {"throw { message: Symbol('m') }", ": Symbol(m): "},
		"bigint":             {"throw 10n", ": threw 10n: "},
		"number":             {"throw 10", ": threw 10: "},
		"empty string":       {"throw ''", ": threw an empty string: "},
		"bare error":         {"throw new Error()", "})()}: Error: evaluation failed"},
		"empty message":      {"throw { message: '' }", ": threw an object: "},
		"nameless message":   {"throw { name: '', message: 'm' }", "})()}: m: evaluation failed"},
		"messageless name":   {"throw Object.assign(new Error(), { name: 'Custom' })", "…}: Custom: evaluation failed"},
		"null prototype":     {"throw Object.create(null)", ": threw an object: "},
		"instance":           {"throw new (class Point {})()", ": threw an instance of Point: "},
		"function":           {"throw () => 1", ": threw a function: "},
		"array":              {"throw [1]", ": threw an array: "},
		"proxy":              {"throw new Proxy({}, { get() { throw new Error('trap ran') } })", ": threw a proxy: "},
		"multi-line message": {"throw new Error('a\\n\\n  b')", ": Error: a b: "},
		"long message":       {"throw new Error('x'.repeat(1000))", ": Error: " + strings.Repeat("x", 193) + "…: evaluation failed"},
		"giant regexp":       {"new RegExp('['.repeat(1 << 20) + '\\\\')", ": SyntaxError: "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			both := evalInBothKinds(t, expressionTemplate("(() => { "+c.source+" })()"), nil)
			both.expectRefused(t, EvaluationFailed, c.expected)
			expectWellFormedAndBounded(t, both.trusted)
			if len(both.trusted.Error()) > 600 {
				t.Fatalf("unbounded message of %d bytes", len(both.trusted.Error()))
			}
		})
	}
}

func engineValuesIn(err error) []string {
	var found []string
	pending := []error{err}
	for len(pending) > 0 {
		next := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		kind := reflect.TypeOf(next)
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if strings.Contains(kind.PkgPath(), "dop251/goja") {
			found = append(found, kind.String())
		}
		switch wrapped := next.(type) {
		case interface{ Unwrap() error }:
			if inner := wrapped.Unwrap(); inner != nil {
				pending = append(pending, inner)
			}
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		}
	}
	return found
}

func TestAThrowKeepsASnapshotAsItsCause(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var out any
		err := mustTemplate(t, sb, expressionTemplate("(() => { throw new RangeError('far') })()")).Eval(t.Context(), &out, nil, "v")
		var caught *Thrown
		if !errors.As(err, &caught) {
			t.Fatalf("expected a thrown snapshot in %v", err)
		}
		expectEqual(t, caught.Message, "RangeError: far")
		expectEqual(t, caught.Value, map[string]any{})
		err = mustTemplate(t, sb, expressionTemplate("(() => { throw { get message() { throw 'inner' } } })()")).Eval(t.Context(), &out, nil, "v")
		if !errors.As(err, &caught) {
			t.Fatalf("expected a thrown snapshot in %v", err)
		}
		expectEqual(t, caught.Message, unreadableThrow)
		expectEqual(t, caught.Value, nil)
	})
}

func TestNoEngineValueEscapesAnEval(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		cases := map[string]struct {
			source string
			vars   map[string]any
		}{
			"thrown error":       {source: "(() => { throw new Error('e') })()"},
			"thrown primitive":   {source: "(() => { throw 10n })()"},
			"unreadable message": {source: "(() => { throw { get message() { throw new Error('inner') } } })()"},
			"overflowing getter": {source: "(() => { throw { get message() { return (function f() { return f() })() } } })()"},
			"throwing getter":    {source: "({ get a() { throw new Error('g') } })"},
			"deep recursion":     {source: "(function f() { return f() })()"},
			"refused JSON":       {source: "JSON.parse('['.repeat(5000))"},
			"engine panic":       {source: "'-'.repeat(width)", vars: map[string]any{"width": 1 << 52}},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				var out any
				err := mustTemplate(t, sb, expressionTemplate(c.source)).Eval(t.Context(), &out, c.vars, "v")
				expectError(t, err, EvaluationFailed)
				if found := engineValuesIn(err); len(found) > 0 {
					t.Fatalf("%v reaches %v", err, found)
				}
			})
		}
	})
}

func TestAThrowingGetterNamesItsResultPath(t *testing.T) {
	cases := map[string]struct {
		source, expected string
	}{
		"nested key":   {"({ a: { get b() { throw new Error('x') } } })", ": reading result.a.b: Error: x: "},
		"array item":   {"Object.defineProperty([1, 2], 1, { get() { throw 'no' } })", ": reading result[1]: threw no: "},
		"constructor":  {"({ a: Object.create({ get constructor() { throw new TypeError('c') } }) })", ": reading result.a: TypeError: c: "},
		"named oddly":  {"({ a: new (class { static get name() { throw new Error('n') } })() })", ": reading result.a: Error: n: "},
		"object names": {"({ a: Object.create({ constructor: { name: { toString() { throw new Error('ran') } } } }) })", "result.a is an object with a custom prototype"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			evalInBothKinds(t, "v: \"${ "+c.source+" }\"\n", nil).expectRefused(t, EvaluationFailed, "$.v: ", c.expected)
		})
	}
}

func thrownValue(t *testing.T, sb *Sandbox, source string) any {
	t.Helper()
	var out any
	err := mustTemplate(t, sb, expressionTemplate("(() => { "+source+" })()")).Eval(t.Context(), &out, nil, "v")
	var caught *Thrown
	if !errors.As(err, &caught) {
		t.Fatalf("expected a thrown snapshot in %v", err)
	}
	return caught.Value
}

func TestAThrownValueHoldsAsManyValuesAsAResult(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		kept, isList := thrownValue(t, sb, "throw Array(1048575).fill(1)").([]any)
		if !isList || len(kept) != 1048575 {
			t.Fatalf("a thrown list of 1048575 items was not kept whole: %T of %d", kept, len(kept))
		}
		expectEqual(t, thrownValue(t, sb, "throw Array(1048576).fill(1)"), nil)
	})
}

func TestAThrownValueCountsEveryListedKeyOnce(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		kept, isList := thrownValue(t, sb, "throw Array(1048572).fill(0).concat([{ a: undefined, b: 1 }])").([]any)
		if !isList || len(kept) != 1048573 {
			t.Fatalf("a thrown list at the cap was not kept whole: %T of %d", kept, len(kept))
		}
		expectEqual(t, kept[len(kept)-1], map[string]any{"b": float64(1)})
		expectEqual(t, thrownValue(t, sb, "throw Array(1048572).fill(0).concat([{ a: undefined, b: 1, c: () => 1 }])"), nil)
	})
}

func TestAThrownValueReadsWhatJSONWouldWrite(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got := thrownValue(t, sb, `const e = new RangeError('r'); e.status = 404; e.cycle = e; throw { list: [() => 1, undefined, Symbol('s'), NaN, 10n, '\ud800', 2], text: '\ud800', ['\udc00']: 1, f() {}, u: undefined, e, n: -0, big: 2 ** 60, p: new Proxy({}, {}) }`)
		expectEqual(t, got, map[string]any{
			"list": []any{nil, nil, nil, nil, nil, nil, float64(2)},
			"text": nil,
			"e":    map[string]any{"status": float64(404), "cycle": nil},
			"n":    float64(0),
			"big":  float64(1 << 60),
			"p":    nil,
		})
	})
}
