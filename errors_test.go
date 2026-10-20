package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var everyKind = []error{ExpressionError, InvalidExpression, InvalidTarget, MissingPath, EvaluationFailed, CoercionFailed, Closed, Unsupported, Unavailable, InvalidInput, MemoryLimit, WallLimit, CallDepthLimit, EvaluatorDied}

func TestACancelledEvalKeepsItsContextCause(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\n")
		var out any
		var err = resolveInto(within(t, 100*time.Millisecond), &out, document.At("spin"))
		expectError(t, err, EvaluationFailed, "$.spin", "deadline exceeded")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in %v", err)
		}
		done, cancel := context.WithCancel(t.Context())
		cancel()
		err = resolveInto(done, &out, document.At("spin"))
		expectError(t, err, EvaluationFailed, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled in %v", err)
		}
		_, err = sb.MakeExpressionFromJaml(done, "v: 1\n")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled from a cancelled construction, got %v", err)
		}
		var cause = errors.New("the caller gave up")
		withCause, cancelWithCause := context.WithCancelCause(t.Context())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancelWithCause(cause)
		}()
		err = resolveInto(withCause, &out, document.At("spin"))
		if !errors.Is(err, cause) {
			t.Fatalf("expected the context's cause in %v", err)
		}
	})
}

func TestATrustedSandboxIsNeverUnavailable(t *testing.T) {
	var sb = mustSandbox(t, trustedKind)
	var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\nthrown: ${ (() => { throw new Error('x') })() }\n")
	done, cancel := context.WithCancel(t.Context())
	cancel()
	var out any
	_, constructed := sb.MakeExpressionFromJaml(done, "v: 1\n")
	var failures = []error{
		resolveInto(within(t, 50*time.Millisecond), &out, document.At("spin")),
		resolveInto(done, &out, document.At("thrown")),
		resolveInto(t.Context(), &out, document.At("thrown")),
		resolveInto(t.Context(), &out, document.At("absent")),
		constructed,
	}
	for _, err := range failures {
		expectError(t, err, ExpressionError)
		if errors.Is(err, Unavailable) {
			t.Fatalf("a trusted sandbox reported an unavailable evaluator: %v", err)
		}
	}
	_, err := MakeTrustedSandbox(done)
	if err != nil {
		t.Fatalf("a trusted sandbox starts nothing, yet: %v", err)
	}
}

func TestAMemoryBombIsAMemoryLimit(t *testing.T) {
	var document = mustJaml(t, mustIsolated(t, WithMemoryLimit(64<<20)), "v: \"${ (() => { const a = []; for (;;) a.push({k: a.length}) })() }\"\n")
	var out any
	var err = resolveInto(within(t, 20*time.Second), &out, document.At("v"))
	expectError(t, err, MemoryLimit, "$.v: exceeded the memory limit of 67108864 bytes: evaluation failed: expression error")
	expectError(t, err, EvaluationFailed)
}

func TestARuntimeOutOfMemoryIsAMemoryLimit(t *testing.T) {
	if !errors.Is(_MemoryExhausted("evaluator died (exit status 2): "+runtimeOutOfMemory), MemoryLimit) {
		t.Fatal("a runtime out-of-memory death is not a MemoryLimit")
	}
	if errors.Is(fail(evaluationFailed, "boom"), MemoryLimit) {
		t.Fatal("every evaluation failure matches MemoryLimit")
	}
}

func TestErrorsKeepTheirMessageFormat(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(t.Context(), &out, mustJaml(t, sb, "v: ${ nope }\n"))
		expectEqual(t, err.Error(), "$.v: ${nope}: ReferenceError: nope is not defined: evaluation failed: expression error")
		_, err = sb.MakeExpressionFromJaml(t.Context(), "v: ${ 1 + }\n")
		expectError(t, err, InvalidExpression, "$.v: ${1 +}: syntax error")
	})
}

type _ShapeWithAList struct {
	A struct {
		B []int `json:"b"`
	} `json:"a"`
}

func TestACoercionFailureNamesTheFailingField(t *testing.T) {
	var text = "a:\n  b: [1, x]\nc:\n  d: {e: [1, 2, {f: 1}]}\n"
	var both = _Agreement{text: text}.eval(t, func() any { return new(_ShapeWithAList) })
	both.expectRefused(t, CoercionFailed, "$.a.b[1]: json: cannot unmarshal string")
	var mismatch *json.UnmarshalTypeError
	if !errors.As(both.trusted, &mismatch) || !errors.As(both.isolated, &mismatch) {
		t.Fatalf("expected *json.UnmarshalTypeError in %v", both.trusted)
	}
	_Agreement{text: text, path: []string{"c"}}.eval(t, func() any { return new(map[string]map[string][]int) }).expectRefused(t, CoercionFailed, "$.c.d.e[2]: json: cannot unmarshal object")
}

var errPicky = errors.New("picky refuses")

type _Picky struct{}

func (*_Picky) UnmarshalJSON([]byte) error {
	return errPicky
}

func TestACustomUnmarshalersErrorIsReachable(t *testing.T) {
	var both = _Agreement{text: "v: ${ 1 }\n", path: []string{"v"}}.eval(t, func() any { return new(_Picky) })
	both.expectRefused(t, CoercionFailed, "$.v: picky refuses")
	if !errors.Is(both.trusted, errPicky) || !errors.Is(both.isolated, errPicky) {
		t.Fatalf("expected the unmarshaler's error in %v", both.trusted)
	}
}

func expectTheSameChildAfter(t *testing.T, sb *Sandbox, run func()) {
	t.Helper()
	if _, isIsolated := sb.engine.(*_IsolatedEngine); !isIsolated {
		run()
		return
	}
	var before = childPID(t, sb)
	run()
	if after := childPID(t, sb); after != before {
		t.Fatalf("the evaluator was replaced: %d became %d", before, after)
	}
}

func evalOrHang(t *testing.T, document *Expression, ctx context.Context, vars map[string]any) error {
	t.Helper()
	var returned = make(chan error, 1)
	go func() {
		var out any
		returned <- resolveInto(valued(ctx, vars), &out, document.At("v"))
	}()
	select {
	case err := <-returned:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the Eval never returned")
		return nil
	}
}

func TestAThrowReadPastItsContextFailsWithTheCauseAndFreesItsSlot(t *testing.T) {
	var cases = map[string]string{
		"spinning message":    "(() => { throw { get message() { for (;;) {} } } })()",
		"spinning field":      "(() => { throw { message: 'm', get x() { for (;;) {} } } })()",
		"spinning both":       "(() => { throw { get message() { for (;;) {} }, get x() { for (;;) {} } } })()",
		"spinning error name": "(() => { const e = new Error('m'); Object.defineProperty(e, 'name', { get() { for (;;) {} } }); throw e })()",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			eachKind(t, func(t *testing.T, sb *Sandbox) {
				var document = mustJaml(t, sb, expressionTemplate(source))
				var err error
				expectTheSameChildAfter(t, sb, func() { err = evalOrHang(t, document, within(t, 200*time.Millisecond), nil) })
				expectError(t, err, EvaluationFailed, "deadline exceeded")
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected context.DeadlineExceeded in %v", err)
				}
				var caught *Thrown
				if errors.As(err, &caught) {
					t.Fatalf("a throw read past its context still produced %+v", *caught)
				}
				var two int
				if err := resolveInto(within(t, 5*time.Second), &two, mustJaml(t, sb, "v: ${ 1 + 1 }\n").At("v")); err != nil {
					t.Fatalf("the next Eval after the spin: %v", err)
				}
			}, WithMaxConcurrency(1))
		})
	}
}

func TestEveryRefusalOfTheCallersInputIsInvalidInput(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "v: ${ 1 }\nplain: 1\n")
		var call = mustExpression(t, sb, "(a) => 1")
		var out any
		for name, err := range map[string]error{
			"a value that is not data":     resolveInto(valued(t.Context(), map[string]any{"bad": make(chan int)}), &out, document.At("v")),
			"a value too deep":             resolveInto(valued(t.Context(), map[string]any{"bad": nestedVar(4097)}), &out, document.At("plain")),
			"a refused value name":         resolveInto(valued(t.Context(), map[string]any{"Math": 1}), &out, document.At("v")),
			"a value not in UTF-8":         resolveInto(valued(t.Context(), map[string]any{"bad": "x\xffy"}), &out, document.At("v")),
			"a value name not in UTF-8":    resolveInto(valued(t.Context(), map[string]any{"b\xffad": 1}), &out, document.At("v")),
			"an argument that is not data": resolveInto(t.Context(), &out, call, func() {}),
			"an argument not in UTF-8":     resolveInto(t.Context(), &out, call, "x\xffy"),
			"a time limit not positive":    resolveInto(WithWallLimit(t.Context(), 0), &out, document.At("v")),
		} {
			expectInvalidInput(t, name, err)
		}
	})
	_SandboxKinds{isolatedKind, unprotectedKind}.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var huge = strings.Repeat("x", 5<<20)
		expectInvalidInput(t, "a request past the cap", resolveInto(valued(t.Context(), map[string]any{"big": huge}), &out, mustJaml(t, sb, "v: ${ big.length }\n").At("v")))
	}, WithMemoryLimit(16<<20))
}

func expectInvalidInput(t *testing.T, name string, err error) {
	t.Helper()
	var failure *Failure
	if !errors.Is(err, InvalidInput) || errors.Is(err, EvaluationFailed) || !errors.As(err, &failure) || failure.Path != "$" {
		t.Fatalf("%s: want an InvalidInput at $ and nothing under EvaluationFailed, got %v (%+v)", name, err, failure)
	}
}

func TestRecursionPastTheCallDepthLimitIsCallDepthLimit(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(t.Context(), &out, mustJaml(t, sb, "v: ${ (function f(n) { return f(n + 1) })(0) }\n").At("v"))
		expectError(t, err, CallDepthLimit, "$.v: ")
		expectFailure(t, err, "$.v", "exceeded the maximum call depth of 100")
		if !errors.Is(err, EvaluationFailed) || errors.Is(err, WallLimit) || errors.Is(err, MemoryLimit) || errors.Is(err, EvaluatorDied) {
			t.Fatalf("a call depth refusal matched the wrong kinds: %v", err)
		}
	}, WithCallDepthLimit(100))
}
