//go:build unix && !ios

package jaml

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func killChild(t *testing.T, sb *Sandbox) {
	t.Helper()
	var child = currentChild(t, sb)
	_ = child.cmd.Process.Kill()
	expectDeath(t, child)
}

func TestAQueuedEvalKeepsItsDeadline(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 }\n")
		var spinning = make(chan error, 1)
		go func() {
			var out any
			spinning <- resolveInto(within(t, 3*time.Second), &out, document.At("spin"))
		}()
		time.Sleep(100 * time.Millisecond)
		var started = time.Now()
		expectError(t, evalBounded(t, document, within(t, 100*time.Millisecond), "ok"), EvaluationFailed, "cancelled", "deadline exceeded")
		if waited := time.Since(started); waited > time.Second {
			t.Fatalf("a queued Eval waited %s past its 100ms deadline", waited)
		}
		expectError(t, <-spinning, EvaluationFailed, "$.spin: cancelled: context deadline exceeded")
	}, WithMaxConcurrency(1))
}

func TestADoneContextLeavesTheEvaluatorAlone(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	var before = childPID(t, sb)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out int
	expectError(t, resolveInto(ctx, &out, document.At("v")), EvaluationFailed, "cancelled")
	expectEqual(t, childPID(t, sb), before)
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 1)
	expectEqual(t, childPID(t, sb), before)
}

const (
	silentLimit          = 16<<20 + 1
	foreignGreetingLimit = 16<<20 + 2
	futureProtocolLimit  = 16<<20 + 3
	malformedAnswerLimit = 16<<20 + 4
	emptyAnswerLimit     = 16<<20 + 5
	crashAtStartLimit    = 16<<20 + 6
	panicAtStartLimit    = 16<<20 + 7
	oversizedAnswerLimit = 16<<20 + 8
	olderProtocolLimit   = 16<<20 + 9
	slowStartLimit       = 64<<20 + 9
	slowStart            = time.Second
)

var _ = startSlowly()

func startSlowly() bool {
	if os.Getenv(evaluatorMarker) != "1" || os.Getenv(evaluatorMemoryLimit) != strconv.Itoa(slowStartLimit) {
		return false
	}
	time.Sleep(slowStart)
	return true
}

var _ = impersonateAnEvaluator()

func impersonateAnEvaluator() bool {
	if os.Getenv(evaluatorMarker) != "1" {
		return false
	}
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(silentLimit), strconv.Itoa(foreignGreetingLimit), strconv.Itoa(futureProtocolLimit), strconv.Itoa(malformedAnswerLimit), strconv.Itoa(emptyAnswerLimit), strconv.Itoa(crashAtStartLimit), strconv.Itoa(panicAtStartLimit), strconv.Itoa(oversizedAnswerLimit), strconv.Itoa(olderProtocolLimit):
	default:
		return false
	}
	var requests = os.NewFile(requestsFD, "impostor-requests")
	var replies = os.NewFile(responsesFD, "impostor-replies")
	var greeting = _Hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: os.Getenv(evaluatorNonce)}
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(silentLimit):
	case strconv.Itoa(crashAtStartLimit):
		os.Exit(3)
	case strconv.Itoa(panicAtStartLimit):
		panic("an impostor's init")
	case strconv.Itoa(foreignGreetingLimit):
		_, _ = replies.WriteString("hello from some other program\n")
		os.Exit(0)
	case strconv.Itoa(futureProtocolLimit):
		greeting.Version = 999
		greetPlainly(replies, greeting)
	case strconv.Itoa(olderProtocolLimit):
		greeting.Version = 10
		greetPlainly(replies, greeting)
	case strconv.Itoa(malformedAnswerLimit):
		answerBadly(requests, replies, greeting, "{\"job\": %d, \"data\": nope}")
	case strconv.Itoa(emptyAnswerLimit):
		answerBadly(requests, replies, greeting, "{\"job\": %d}")
	case strconv.Itoa(oversizedAnswerLimit):
		var frame = `{"job": %d, "data": "` + strings.Repeat("x", (oversizedAnswerLimit/4)-len(`{"job": 2, "data": ""}`)+1) + "\"}"
		answerBadly(requests, replies, greeting, frame)
	}
	_, _ = io.Copy(io.Discard, requests)
	os.Exit(0)
	return true
}

func greetPlainly(replies *os.File, greeting _Hello) {
	encoded, _ := wireJSON(greeting)
	_, _ = replies.Write(append(encoded, '\n'))
}

func answerBadly(requests, replies *os.File, greeting _Hello, badAnswer string) {
	greetPlainly(replies, greeting)
	var incoming = framesFor(defaultMemoryLimit).requests(requests)
	for {
		var asked _Request
		size, err := incoming.size()
		if err != nil || incoming.into(size, &asked) != nil {
			return
		}
		switch asked.Op {
		case opBuild:
			_, _ = replies.Write(headed(fmt.Appendf(nil, "{\"job\": %d, \"built\": true}", asked.Job)))
		case opServe:
			_, _ = replies.Write(headed(fmt.Appendf(nil, badAnswer, asked.Job)))
		}
	}
}

func TestAForeignExecutableIsUnsupported(t *testing.T) {
	_, err := MakeSecureSandbox(within(t, 10*time.Second), WithMemoryLimit(foreignGreetingLimit))
	expectError(t, err, Unsupported, "is not a jaml evaluator")
}

func TestAReplacedExecutableIsUnsupported(t *testing.T) {
	_, err := MakeSecureSandbox(within(t, 10*time.Second), WithMemoryLimit(futureProtocolLimit))
	expectError(t, err, Unsupported, fmt.Sprintf("speaks jaml protocol v999, want v%d", protocolVersion))
}

func TestAnOlderEvaluatorIsNamedByItsVersion(t *testing.T) {
	_, err := MakeSecureSandbox(within(t, 10*time.Second), WithMemoryLimit(olderProtocolLimit))
	expectError(t, err, Unsupported, fmt.Sprintf("speaks jaml protocol v10, want v%d", protocolVersion))
}

func TestASilentExecutableEndsWithTheContext(t *testing.T) {
	var started = time.Now()
	_, err := MakeSecureSandbox(within(t, 300*time.Millisecond), WithMemoryLimit(silentLimit))
	expectError(t, err, EvaluationFailed, "starting the evaluator", "cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("a silent evaluator held construction for %s", waited)
	}
}

func TestASilentExecutableEndsWithItsStartDeadline(t *testing.T) {
	var started = time.Now()
	_, err := startLife(_ChildSpec{lifetime: t.Context(), limit: silentLimit, startDeadline: 200 * time.Millisecond, buildTimeout: defaultBuildTimeout})
	expectError(t, err, EvaluationFailed, "starting the evaluator", "took longer than 200ms to start")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in %v", err)
	}
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("a silent evaluator held construction for %s", waited)
	}
}

func TestAShortBuildTimeoutDoesNotBoundStartingTheEvaluator(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(slowStartLimit), WithBuildTimeout(slowStart/4))
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 1 }\n"), nil, "v"), 1)
}

func TestAShortBuildTimeoutDoesNotBoundARespawn(t *testing.T) {
	var sb = mustIsolated(t, WithMaxConcurrency(8))
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	respawnAs(t, sb, _ChildSpec{limit: slowStartLimit, startDeadline: evaluatorStartDeadline, buildTimeout: slowStart / 4})
	killChild(t, sb)
	var answers = make(chan error, 6)
	for range 6 {
		go func() {
			var out int
			answers <- resolveInto(within(t, 30*time.Second), &out, document.At("v"))
		}()
	}
	for range 6 {
		if err := <-answers; err != nil {
			t.Fatalf("a job waiting behind a slow respawn was refused: %v", err)
		}
	}
}

func TestAMalformedAnswerKillsTheEvaluatorWithItsCause(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(malformedAnswerLimit))
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	var process = currentChild(t, sb)
	var started = time.Now()
	expectError(t, evalBounded(t, document, within(t, 30*time.Second), "v"), EvaluationFailed, "$.v", "reading the evaluator's answer", "invalid character")
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("a malformed answer was noticed only after %s", waited)
	}
	expectDeath(t, process)
}

func TestAnAnswerWithNeitherDataNorFailureKillsTheEvaluator(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(emptyAnswerLimit))
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	var process = currentChild(t, sb)
	expectError(t, evalBounded(t, document, within(t, 30*time.Second), "v"), EvaluationFailed, "$.v", "neither data nor a failure")
	expectDeath(t, process)
}

func respawnAs(t *testing.T, sb *Sandbox, spec _ChildSpec) {
	t.Helper()
	spec.lifetime = sb.lifetime
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.start = func() (*_Life, error) { return startLife(spec) }
}

func TestTheSandboxContextEndingInterruptsARespawn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sb, err := MakeSecureSandbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	respawnAs(t, sb, _ChildSpec{limit: silentLimit, startDeadline: evaluatorStartDeadline, buildTimeout: defaultBuildTimeout})
	killChild(t, sb)
	var evaluating = make(chan error, 1)
	go func() {
		var out int
		evaluating <- resolveInto(t.Context(), &out, document.At("v"))
	}()
	time.Sleep(200 * time.Millisecond)
	var started = time.Now()
	cancel()
	select {
	case err := <-evaluating:
		expectError(t, err, Closed)
	case <-time.After(5 * time.Second):
		t.Fatal("the respawning Eval outlived its sandbox")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("an Eval behind a respawn took %s to see its sandbox end", elapsed)
	}
}

func evaluatorCPU(t *testing.T, idle time.Duration) time.Duration {
	ctx, cancel := context.WithCancel(t.Context())
	sb, err := MakeSecureSandbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var process = currentChild(t, sb)
	time.Sleep(idle)
	cancel()
	expectDeath(t, process)
	return process.cmd.ProcessState.UserTime() + process.cmd.ProcessState.SystemTime()
}

func TestAnIdleEvaluatorSleeps(t *testing.T) {
	var startup = evaluatorCPU(t, 0)
	var idling = evaluatorCPU(t, 2*time.Second)
	if spent := idling - startup; spent > 15*time.Millisecond {
		t.Fatalf("an idle evaluator spent %s of CPU in 2s (startup alone %s)", spent, startup)
	}
}

func TestADeathNamesItsReasonNotItsStack(t *testing.T) {
	var dump = "fatal error: runtime: out of memory\n\nruntime stack:\nruntime.throw({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:1101\n\ngoroutine 1 [running]:\nmain.main()\n"
	var stderr = &_HeadBuffer{limit: stderrHeadBytes}
	_, _ = stderr.Write([]byte("jaml evaluator: something first\n" + dump + strings.Repeat("goroutine 9 [chan receive]:\n", 1000)))
	expectEqual(t, stderr.reason(), "fatal error: runtime: out of memory")
	var plain = &_HeadBuffer{limit: stderrHeadBytes}
	_, _ = plain.Write([]byte("jaml evaluator: reading request: bad\n"))
	expectEqual(t, plain.reason(), "jaml evaluator: reading request: bad")
}

func TestARespawnFailsAsConstructionWouldHave(t *testing.T) {
	for name, limit := range map[string]int64{"foreign greeting": foreignGreetingLimit, "future protocol": futureProtocolLimit, "crash before greeting": crashAtStartLimit} {
		t.Run(name, func(t *testing.T) {
			_, constructed := MakeSecureSandbox(within(t, 10*time.Second), WithMemoryLimit(limit))
			var sb = mustIsolated(t)
			var document = mustJaml(t, sb, "a: 1\n")
			expectEqual(t, isolatedEvalAs[int](t, document, nil, "a"), 1)
			respawnAs(t, sb, _ChildSpec{limit: _MemoryLimit(limit), startDeadline: evaluatorStartDeadline, buildTimeout: defaultBuildTimeout})
			killChild(t, sb)
			var out any
			var respawned = resolveInto(within(t, 10*time.Second), &out, document.At("a"))
			if name == "crash before greeting" {
				expectError(t, respawned, EvaluationFailed, "evaluator died (exit status 3)")
			} else {
				expectError(t, respawned, Unsupported)
			}
			expectEqual(t, respawned.Error(), constructed.Error())
			for _, kind := range everyKind {
				if errors.Is(respawned, kind) != errors.Is(constructed, kind) {
					t.Fatalf("a respawn and a construction disagree on %v:\nrespawn:      %v\nconstruction: %v", kind, respawned, constructed)
				}
			}
		})
	}
}

func TestEvalsWithADoneContextStartNoEvaluator(t *testing.T) {
	var sb = mustIsolated(t)
	var document = mustJaml(t, sb, "a: 1\n")
	killChild(t, sb)
	var before = liveChildren(t)
	done, stop := context.WithCancel(t.Context())
	stop()
	var out any
	for range 40 {
		var err = resolveInto(done, &out, document.At("a"))
		expectError(t, err, EvaluationFailed, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled in %v", err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if after := liveChildren(t); after > before {
		t.Fatalf("40 Evals with a done context left %d evaluators running", after-before)
	}
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "a"), 1)
}

func TestAHandshakeCutShortEndsItsEvaluator(t *testing.T) {
	var before = liveChildren(t)
	for range 10 {
		_, err := MakeSecureSandbox(within(t, 50*time.Millisecond), WithMemoryLimit(silentLimit))
		expectError(t, err, EvaluationFailed, "cancelled")
	}
	expectChildrenBackTo(t, before)
}

func hostCPU(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func hostileText() string {
	return "v: '${[" + strings.Repeat("("+strings.Repeat("-1+", 4095)+"0),", 5) + "]}'\n"
}

func TestAHostileTemplateCostsTheHostNothingToCompile(t *testing.T) {
	var sb = mustIsolated(t)
	goruntime.GC()
	spentBefore, started := hostCPU(t), time.Now()
	_, err := sb.MakeExpressionFromJaml(within(t, 200*time.Millisecond), hostileText())
	waited, spent := time.Since(started), hostCPU(t)-spentBefore
	expectError(t, err, EvaluationFailed, "cancelled", "deadline exceeded")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in %v", err)
	}
	if waited > time.Second {
		t.Fatalf("constructing a hostile document held the host for %s past its 200ms context", waited)
	}
	if spent > 150*time.Millisecond {
		t.Fatalf("the host spent %s of CPU on a document it should only carry", spent)
	}
}

func TestAHostileBuildIsBoundedByTheBuildTimeout(t *testing.T) {
	var sb = mustIsolated(t, WithBuildTimeout(200*time.Millisecond))
	var started = time.Now()
	_, err := sb.MakeExpressionFromJaml(t.Context(), hostileText())
	expectError(t, err, EvaluationFailed, "cancelled: the build took longer than its timeout of 200ms")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in %v", err)
	}
	if waited := time.Since(started); waited > 1500*time.Millisecond {
		t.Fatalf("a hostile build held construction for %s past its 200ms build timeout", waited)
	}
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, sb, "v: ${ 6 }\n"), nil, "v"), 6)
	_, err = sb.MakeExpressionFromJaml(t.Context(), "v: '${"+strings.Repeat("a=>", 21000)+"1}'\n")
	expectError(t, err, InvalidExpression, "$.v", "arrow functions nest more than 512 deep")
}

func listOfPlainServers(count int) string {
	var written strings.Builder
	written.WriteString("expr: ${ 1 + 1 }\nitems:\n")
	for index := range count {
		written.WriteString("  - {name: item-" + strconv.Itoa(index) + ", port: " + strconv.Itoa(index) + ", host: host-" + strconv.Itoa(index) + ".example.com}\n")
	}
	return written.String()
}

func TestABuildPastTheLimitFailsConstruction(t *testing.T) {
	var text = listOfPlainServers(50000)
	if len(text) >= 4<<20 {
		t.Fatalf("the document is %d bytes, not under the 4 MiB request cap", len(text))
	}
	_, err := mustIsolated(t, WithMemoryLimit(16<<20), WithBuildTimeout(30*time.Second)).MakeExpressionFromJaml(within(t, 60*time.Second), text)
	expectError(t, err, MemoryLimit, "exceeded the memory limit of 16777216 bytes")
	expectError(t, err, EvaluationFailed)
}

func TestADocumentPastTheRequestCapIsRefusedBeforeItsBuild(t *testing.T) {
	for name, text := range map[string]string{
		"one huge value":     "v: " + strings.Repeat("x", 40<<20) + "\n",
		"many plain servers": listOfPlainServers(100000),
	} {
		t.Run(name, func(t *testing.T) {
			var sb = mustIsolated(t, WithMemoryLimit(16<<20), WithBuildTimeout(30*time.Second))
			var before = childPID(t, sb)
			_, err := sb.MakeExpressionFromJaml(within(t, 60*time.Second), text)
			expectError(t, err, EvaluationFailed, "the request is larger than 4194304 bytes, mostly the document")
			expectEqual(t, childPID(t, sb), before)
		})
	}
}

func TestBuiltTemplatesCountAgainstTheCeiling(t *testing.T) {
	var big = "big: \"${ (() => { const held = 'x'.repeat(48 << 20); let n = 0; for (let i = 0; i < 1e6; i++) n += i; return held.length })() }\"\n"
	var alone = mustIsolated(t, WithMemoryLimit(64<<20))
	var before = childPID(t, alone)
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, alone, big), nil, "big"), 48<<20)
	expectEqual(t, childPID(t, alone), before)
	var crowded = mustIsolated(t, WithMemoryLimit(64<<20))
	var resident = make([]*Expression, 32)
	for index := range resident {
		resident[index] = mustJaml(t, crowded, "v: "+strings.Repeat("y", 1<<20)+"\n")
	}
	before = childPID(t, crowded)
	expectEqual(t, isolatedEvalAs[int](t, mustJaml(t, crowded, big), nil, "big"), 48<<20)
	if childPID(t, crowded) == before {
		t.Fatal("32 MiB of built documents and a 48 MiB Eval fit under a 64 MiB ceiling")
	}
	expectEqual(t, len(isolatedEvalAs[string](t, resident[0], nil, "v")), 1<<20)
}

func TestTheSandboxContextEndsEverything(t *testing.T) {
	var before = liveChildren(t)
	for _, kind := range sandboxKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			sb, err := kind.make(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var document = mustJaml(t, sb, "v: ${ 1 }\nspin: ${ (() => { while (true) {} })() }\n")
			var compiled = mustExpression(t, sb, "x => x")
			var running = make(chan error, 1)
			go func() {
				var out any
				running <- resolveInto(t.Context(), &out, document.At("spin"))
			}()
			time.Sleep(100 * time.Millisecond)
			cancel()
			expectError(t, <-running, Closed)
			var out any
			expectError(t, resolveInto(t.Context(), &out, document.At("v")), Closed)
			expectError(t, resolveInto(t.Context(), &out, compiled, 1), Closed)
			expectError(t, resolveInto(t.Context(), &out, compiled), Closed)
			_, err = sb.MakeExpressionFromJaml(t.Context(), "v: 1\n")
			expectError(t, err, Closed)
			_, err = sb.MakeExpression(t.Context(), "1")
			expectError(t, err, Closed)
		})
	}
	expectChildrenBackTo(t, before)
}

func TestATemplateContextEndingClosesOnlyThatTemplate(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		ctx, cancel := context.WithCancel(t.Context())
		ending, err := sb.MakeExpressionFromJaml(ctx, "v: ${ 1 }\nspin: ${ (() => { while (true) {} })() }\n")
		if err != nil {
			t.Fatal(err)
		}
		endingExpression, err := sb.MakeExpression(ctx, "1")
		if err != nil {
			t.Fatal(err)
		}
		var staying = mustJaml(t, sb, "v: ${ 2 }\n")
		var running = make(chan error, 1)
		go func() {
			var out any
			running <- resolveInto(t.Context(), &out, ending.At("spin"))
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		expectError(t, <-running, Closed)
		var out any
		expectError(t, resolveInto(t.Context(), &out, ending.At("v")), Closed)
		expectError(t, resolveInto(t.Context(), &out, endingExpression), Closed)
		expectEqual(t, mustResolve[int](t, t.Context(), staying.At("v")), 2)
	})
}

func TestATemplateWhoseContextEndsDuringConstructionCloses(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for attempt := range 50 {
			ctx, cancel := context.WithCancel(t.Context())
			go func() {
				time.Sleep(time.Duration(attempt%10) * time.Millisecond)
				cancel()
			}()
			document, err := sb.MakeExpressionFromJaml(ctx, "v: ${ 1 }\n")
			if err != nil {
				expectError(t, err, EvaluationFailed, "cancelled")
				continue
			}
			<-ctx.Done()
			var out int
			expectError(t, resolveInto(within(t, time.Second), &out, document.At("v")), Closed)
		}
	})
}

func dropATemplate(t *testing.T, sb *Sandbox) *_RemoteUnit {
	var document = mustJaml(t, sb, "v: ${ 1 }\n")
	return document.held.keep.unit.(*_RemoteUnit)
}

func residencyOf(child *_Life, u *_RemoteUnit) *_Residency {
	child.wire.mu.Lock()
	defer child.wire.mu.Unlock()
	return child.wire.residents[u]
}

func TestADroppedTemplateIsForgottenByTheEvaluator(t *testing.T) {
	var sb = mustIsolated(t)
	var dropped = dropATemplate(t, sb)
	var child = currentChild(t, sb)
	var build = residencyOf(child, dropped).build
	var deadline = time.Now().Add(10 * time.Second)
	for residencyOf(child, dropped) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("unit %d outlived its unreachable template", build)
		}
		goruntime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		var served = child.serve(within(t, 10*time.Second), askFor(t, build, _Ask{}))
		if failed, isRefused := served.(_Refused); isRefused && errors.Is(failed.err, errMissing) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the evaluator still serves unit %d: %v", build, served)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUnmadeValuesAreInvalid(t *testing.T) {
	var out int
	for name, sb := range map[string]*Sandbox{"zero": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			_, err := sb.MakeExpressionFromJaml(t.Context(), "v: 1\n")
			expectError(t, err, InvalidExpression, "not made by")
			_, err = sb.MakeExpression(t.Context(), "1")
			expectError(t, err, InvalidExpression, "not made by")
		})
	}
	for name, document := range map[string]*Expression{"zero": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			expectError(t, resolveInto(t.Context(), &out, document.At("v")), InvalidExpression, "not made by")
		})
	}
	for name, compiled := range map[string]*Expression{"zero": {}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			expectError(t, resolveInto(t.Context(), &out, compiled), InvalidExpression, "not made by")
			expectError(t, resolveInto(t.Context(), &out, compiled), InvalidExpression, "not made by")
		})
	}
}
