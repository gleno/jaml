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
	template := mustTemplate(t, sb, "spin: ${ (() => { while (true) {} })() }\n")
	spinning := make(chan error, 1)
	go func() {
		var out any
		spinning <- template.Eval(within(t, busy), &out, nil, "spin")
	}()
	time.Sleep(100 * time.Millisecond)
	return spinning
}

func TestABuildTimeoutBoundsTheBuildNotTheWaitForItsSlot(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		spinning := busyFor(t, sb, time.Second)
		if _, err := sb.Compile(within(t, 5*time.Second), "1"); err != nil {
			t.Fatalf("a compile that waited for its slot longer than its build timeout failed: %v", err)
		}
		<-spinning
		spinning = busyFor(t, sb, time.Second)
		_, err := sb.Compile(within(t, 100*time.Millisecond), "1")
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
	text := manyExpressions(20000)
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for range 3 {
			_, err := sb.MakeTemplate(t.Context(), text)
			expectError(t, err, EvaluationFailed, "cancelled: the build took longer than its timeout of 30ms")
		}
	}, WithBuildTimeout(30*time.Millisecond))
}

type engineFunc func(ctx context.Context, plan blueprint) (unit, error)

func (f engineFunc) build(ctx context.Context, plan blueprint) (unit, error) { return f(ctx, plan) }

type idleUnit struct{}

func (idleUnit) serve(context.Context, work) outcome { return answered{data: []byte("1")} }

func TestABuildThatOutlivesItsCallerIsNeverHandedBack(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	sb := &Sandbox{lifetime: t.Context(), engine: engineFunc(func(context.Context, blueprint) (unit, error) {
		cancel()
		return idleUnit{}, nil
	})}
	_, err := sb.MakeTemplate(ctx, "v: 1\n")
	expectError(t, err, EvaluationFailed, "building: cancelled: context canceled")
}

func TestABuildThatOutlivesItsSandboxIsClosed(t *testing.T) {
	lifetime, end := context.WithCancel(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	sb := &Sandbox{lifetime: lifetime, engine: engineFunc(func(context.Context, blueprint) (unit, error) {
		cancel()
		end()
		return idleUnit{}, nil
	})}
	_, err := sb.Compile(ctx, "1")
	expectError(t, err, Closed)
}

func TestABuildRaceKeepsTheFirstVerdict(t *testing.T) {
	parent, expire := context.WithCancel(t.Context())
	finishedFirst := raceBuild(parent, time.Hour)
	if !finishedFirst.finish(answered{}) {
		t.Fatal("a build that finished before its deadline lost the race")
	}
	expire()
	if _, built := finishedFirst.await().(answered); !built {
		t.Fatalf("the deadline overturned a recorded completion: %#v", finishedFirst.await())
	}
	expiredFirst := raceBuild(t.Context(), time.Nanosecond)
	<-expiredFirst.done()
	if expiredFirst.finish(answered{}) {
		t.Fatal("a completion overturned a recorded deadline")
	}
	failed, isRefused := expiredFirst.await().(refused)
	if !isRefused {
		t.Fatalf("an expired build was not refused: %#v", expiredFirst.await())
	}
	expectError(t, located("", failed.err), EvaluationFailed, "cancelled: the build took longer than its timeout of 1ns")
}

func TestABuildRaceRecordsExactlyOneVerdict(t *testing.T) {
	for range 10000 {
		race := raceBuild(t.Context(), time.Microsecond)
		var won bool
		var finishing sync.WaitGroup
		finishing.Go(func() { won = race.finish(answered{}) })
		finishing.Wait()
		_, built := race.await().(answered)
		if built != won {
			t.Fatalf("the race recorded %#v while finish reported winning=%v", race.await(), won)
		}
	}
}

func TestAPlainDecimalKeyIsSpelledAsAnIndex(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "\"0\":\n  boom: ${ (() => { throw new Error('boom') })() }\n  \"01\": 1\n")
		var out any
		expectError(t, template.Eval(t.Context(), &out, nil, "0", "boom"), EvaluationFailed, "$[0].boom: ", "Error: boom")
		expectError(t, template.Eval(t.Context(), &out, nil, "0", "nope"), MissingPath, `$[0] has no key "nope"`)
		expectError(t, template.Eval(t.Context(), &out, nil, "0"), EvaluationFailed, "$[0].boom: ", "Error: boom")
		expectEqual(t, evalAs[int](t, template, nil, "0", "01"), 1)
	})
}

func TestInvalidUTF8TemplateTextIsRefusedAlikeInBothKinds(t *testing.T) {
	cases := map[string]string{
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
			_, err := sb.MakeTemplate(t.Context(), text)
			expectError(t, err, InvalidTemplate, message)
		}
	})
}

func TestInvalidUTF8ExpressionSourceIsRefusedAlikeInBothKinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		_, err := sb.Compile(t.Context(), "'a\xffb'")
		expectError(t, err, InvalidTemplate, "syntax error at line 1, column 3 of the expression: Invalid UTF-8 character")
		_, err = sb.Compile(t.Context(), "1 +\n 1 // \xff")
		expectError(t, err, InvalidTemplate, "syntax error at line 2, column 7 of the expression: Invalid UTF-8 character")
	})
}

func TestAnInvalidUTF8PathStepIsMissingAlikeInBothKinds(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "\"�\": 1\nk: v\nlist: [1]\n")
		var out any
		expectError(t, template.Eval(t.Context(), &out, nil, "\xff"), MissingPath, `$ has no key "\xff"`)
		expectError(t, template.Eval(t.Context(), &out, nil, "k", "\xff"), MissingPath, `$.k has no key "\xff"`)
		expectError(t, template.Eval(t.Context(), &out, nil, "list", "\xff"), MissingPath, `$.list has no key "\xff"`)
		expectError(t, template.Eval(t.Context(), &out, nil, "nope", "\xff"), MissingPath, `$.nope has no key "\xff"`)
	})
}
