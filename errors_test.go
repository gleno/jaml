package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var everyKind = []error{TemplateError, InvalidTemplate, InvalidTarget, MissingPath, EvaluationFailed, CoercionFailed, Closed, Unsupported, MemoryLimit}

func TestACancelledSecureEvalKeepsItsContextCause(t *testing.T) {
	template := mustSecure(t, "spin: ${ (() => { while (true) {} })() }\n")
	var out any
	err := template.Eval(within(t, 100*time.Millisecond), &out, nil, "spin")
	expectError(t, err, EvaluationFailed, "$.spin", "deadline exceeded")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in %v", err)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	err = template.Eval(done, &out, nil, "spin")
	expectError(t, err, EvaluationFailed, "cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in %v", err)
	}
	_, err = MakeSecureTemplate(done, "v: 1\n")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from a cancelled construction, got %v", err)
	}
}

func TestAMemoryBombIsAMemoryLimit(t *testing.T) {
	template := mustSecure(t, "v: \"${ 'x'.repeat(1e9).length }\"\n", WithMemoryLimit(64<<20))
	var out any
	err := template.Eval(within(t, 20*time.Second), &out, nil, "v")
	expectError(t, err, MemoryLimit, "$.v: exceeded the memory limit of 67108864 bytes: evaluation failed: template error")
	expectError(t, err, EvaluationFailed)
}

func TestARuntimeOutOfMemoryIsAMemoryLimit(t *testing.T) {
	if !errors.Is(memoryExhausted("evaluator died (exit status 2): "+runtimeOutOfMemory), MemoryLimit) {
		t.Fatal("a runtime out-of-memory death is not a MemoryLimit")
	}
	if errors.Is(fail(EvaluationFailed, "boom"), MemoryLimit) {
		t.Fatal("every evaluation failure matches MemoryLimit")
	}
}

func TestErrorsKeepTheirMessageFormat(t *testing.T) {
	var out any
	err := mustTemplate(t, "v: ${ nope }\n").Eval(&out, nil)
	expectEqual(t, err.Error(), "$.v: ${nope}: ReferenceError: nope is not defined: evaluation failed: template error")
	_, err = MakeTrustedTemplate("v: ${ 1 + }\n")
	expectError(t, err, InvalidTemplate, "$.v: ${1 +}: syntax error")
}

type shapeWithAList struct {
	A struct {
		B []int `json:"b"`
	} `json:"a"`
}

func TestACoercionFailureNamesTheFailingField(t *testing.T) {
	text := "a:\n  b: [1, x]\nc:\n  d: {e: [1, 2, {f: 1}]}\n"
	both := agreement{text: text}.eval(t, func() any { return new(shapeWithAList) })
	both.expectRefused(t, CoercionFailed, "$.a.b[1]: json: cannot unmarshal string")
	var mismatch *json.UnmarshalTypeError
	if !errors.As(both.trusted, &mismatch) || !errors.As(both.secure, &mismatch) {
		t.Fatalf("expected *json.UnmarshalTypeError in %v", both.trusted)
	}
	agreement{text: text, path: []string{"c"}}.eval(t, func() any { return new(map[string]map[string][]int) }).expectRefused(t, CoercionFailed, "$.c.d.e[2]: json: cannot unmarshal object")
}

var errPicky = errors.New("picky refuses")

type picky struct{}

func (*picky) UnmarshalJSON([]byte) error {
	return errPicky
}

func TestACustomUnmarshalersErrorIsReachable(t *testing.T) {
	both := agreement{text: "v: ${ 1 }\n", path: []string{"v"}}.eval(t, func() any { return new(picky) })
	both.expectRefused(t, CoercionFailed, "$.v: picky refuses")
	if !errors.Is(both.trusted, errPicky) || !errors.Is(both.secure, errPicky) {
		t.Fatalf("expected the unmarshaler's error in %v", both.trusted)
	}
}
