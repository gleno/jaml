package jaml

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func busyFor(t *testing.T, sb *Sandbox, busy time.Duration) <-chan error {
	t.Helper()
	var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\n")
	var spinning = make(chan error, 1)
	go func() {
		var out any
		spinning <- resolveInto(within(t, busy), &out, document.At("spin"))
	}()
	time.Sleep(100 * time.Millisecond)
	return spinning
}

func TestABuildTimeoutBoundsTheBuildNotTheWaitForItsSlot(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var spinning = busyFor(t, sb, time.Second)
		if _, err := sb.MakeExpression(within(t, 5*time.Second), "1"); err != nil {
			t.Fatalf("a compile that waited for its slot longer than its build timeout failed: %v", err)
		}
		<-spinning
		spinning = busyFor(t, sb, time.Second)
		_, err := sb.MakeExpression(within(t, 100*time.Millisecond), "1")
		expectError(t, err, EvaluationFailed, "building: cancelled: context deadline exceeded")
		<-spinning
	}, WithMaxConcurrency(1), WithBuildTimeout(50*time.Millisecond))
}

func manyExpressions(count int) string {
	var written strings.Builder
	for index := range count {
		fmt.Fprintf(&written, "k%d: ${ %d + 1 }\n", index, index)
	}
	return written.String()
}

func TestABuildOverrunningItsTimeoutFailsAlikeInBothKinds(t *testing.T) {
	var text = manyExpressions(20000)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for range 3 {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, EvaluationFailed, "cancelled: the build took longer than its timeout of 30ms")
		}
	}, WithBuildTimeout(30*time.Millisecond))
}

type _EngineFunc func(ctx context.Context, plan _Blueprint) (_Unit, error)

func (f _EngineFunc) build(ctx context.Context, plan _Blueprint) (_Unit, error) { return f(ctx, plan) }

type _IdleUnit struct{}

func (_IdleUnit) serve(context.Context, _Work) _Outcome { return _Answered{data: []byte("1")} }

func TestABuildThatOutlivesItsCallerIsNeverHandedBack(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var sb = &Sandbox{lifetime: t.Context(), engine: _EngineFunc(func(context.Context, _Blueprint) (_Unit, error) {
		cancel()
		return _IdleUnit{}, nil
	})}
	_, err := sb.MakeExpressionFromJaml(ctx, "v: 1\n")
	expectError(t, err, EvaluationFailed, "building: cancelled: context canceled")
}

func TestABuildThatOutlivesItsSandboxIsClosed(t *testing.T) {
	lifetime, end := context.WithCancel(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	var sb = &Sandbox{lifetime: lifetime, engine: _EngineFunc(func(context.Context, _Blueprint) (_Unit, error) {
		cancel()
		end()
		return _IdleUnit{}, nil
	})}
	_, err := sb.MakeExpression(ctx, "1")
	expectError(t, err, Closed)
}

func TestABuildRaceKeepsTheFirstVerdict(t *testing.T) {
	parent, expire := context.WithCancel(t.Context())
	var finishedFirst = raceBuild(parent, time.Hour)
	if !finishedFirst.finish(_Answered{}) {
		t.Fatal("a build that finished before its deadline lost the race")
	}
	expire()
	if _, built := finishedFirst.await().(_Answered); !built {
		t.Fatalf("the deadline overturned a recorded completion: %#v", finishedFirst.await())
	}
	var expiredFirst = raceBuild(t.Context(), time.Nanosecond)
	<-expiredFirst.done()
	if expiredFirst.finish(_Answered{}) {
		t.Fatal("a completion overturned a recorded deadline")
	}
	failed, isRefused := expiredFirst.await().(_Refused)
	if !isRefused {
		t.Fatalf("an expired build was not refused: %#v", expiredFirst.await())
	}
	expectError(t, located("", failed.err), EvaluationFailed, "cancelled: the build took longer than its timeout of 1ns")
}

func TestABuildRaceRecordsExactlyOneVerdict(t *testing.T) {
	for range 10000 {
		var race = raceBuild(t.Context(), time.Microsecond)
		var won bool
		var finishing sync.WaitGroup
		finishing.Go(func() { won = race.finish(_Answered{}) })
		finishing.Wait()
		_, built := race.await().(_Answered)
		if built != won {
			t.Fatalf("the race recorded %#v while finish reported winning=%v", race.await(), won)
		}
	}
}

func TestAPlainDecimalKeyIsSpelledAsAnIndex(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "\"0\":\n  boom: ${ (() => { throw new Error('boom') })() }\n  \"01\": 1\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("0", "boom")), EvaluationFailed, "$[0].boom: ", "Error: boom")
		expectError(t, resolveInto(t.Context(), &out, document.At("0", "nope")), MissingPath, `$[0] has no key "nope"`)
		expectError(t, resolveInto(t.Context(), &out, document.At("0")), EvaluationFailed, "$[0].boom: ", "Error: boom")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("0", "01")), 1)
	})
}

func TestInvalidUTF8TemplateTextIsRefusedAlikeInBothKinds(t *testing.T) {
	var cases = map[string]string{
		"k: \"a\xffb\"\n":   "yaml: invalid leading UTF-8 octet",
		"k: 1\n# \xc3":      "yaml: incomplete UTF-8 octet sequence",
		"k: a\xc3(\n":       "yaml: invalid trailing UTF-8 octet",
		"k: \xc0\xaf\n":     "yaml: invalid length of a UTF-8 sequence",
		"k: \xed\xa0\x80\n": "yaml: invalid Unicode character",
		"k: ${ '\xff' }\n":  "yaml: invalid leading UTF-8 octet",
		"k: [\n  \xff\n":    "yaml: invalid leading UTF-8 octet",
	}
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for text, message := range cases {
			_, err := sb.MakeExpressionFromJaml(t.Context(), text)
			expectError(t, err, InvalidExpression, message)
		}
	})
}

func TestInvalidUTF8ExpressionSourceIsRefusedAlikeInBothKinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.MakeExpression(t.Context(), "'a\xffb'")
		expectError(t, err, InvalidExpression, "syntax error at line 1, column 3 of the expression: Invalid UTF-8 character")
		_, err = sb.MakeExpression(t.Context(), "1 +\n 1 // \xff")
		expectError(t, err, InvalidExpression, "syntax error at line 2, column 7 of the expression: Invalid UTF-8 character")
	})
}

func TestAnInvalidUTF8PathStepIsMissingAlikeInBothKinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "\"�\": 1\nk: v\nlist: [1]\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("\xff")), MissingPath, `$ has no key "\xff"`)
		expectError(t, resolveInto(t.Context(), &out, document.At("k", "\xff")), MissingPath, `$.k has no key "\xff"`)
		expectError(t, resolveInto(t.Context(), &out, document.At("list", "\xff")), MissingPath, `$.list has no key "\xff"`)
		expectError(t, resolveInto(t.Context(), &out, document.At("nope", "\xff")), MissingPath, `$.nope has no key "\xff"`)
	})
}
