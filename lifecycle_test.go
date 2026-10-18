//go:build unix && !ios

package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAQueuedEvalKeepsItsDeadline(t *testing.T) {
	template := mustSecure(t, "spin: ${ (() => { while (true) {} })() }\nok: ${ 1 }\n")
	spinning := make(chan error, 1)
	go func() {
		var out any
		spinning <- template.Eval(within(t, 3*time.Second), &out, nil, "spin")
	}()
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	var out int
	expectError(t, evalBounded(t, template, within(t, 100*time.Millisecond), "ok"), EvaluationFailed, "cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("a queued Eval waited %s past its 100ms deadline", waited)
	}
	_ = out
	<-spinning
}

func TestADoneContextLeavesTheEvaluatorAlone(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n")
	before := evaluatorPID(template)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out int
	expectError(t, template.Eval(ctx, &out, nil, "v"), EvaluationFailed, "cancelled")
	expectEqual(t, evaluatorPID(template), before)
	expectEqual(t, secureEvalAs[int](t, template, nil, "v"), 1)
	expectEqual(t, evaluatorPID(template), before)
}

func dropATemplate(t *testing.T) int {
	template, err := MakeSecureTemplate(context.Background(), "v: ${ 1 }\n")
	if err != nil {
		t.Fatal(err)
	}
	return evaluatorPID(template)
}

func TestADroppedTemplateReleasesItsEvaluator(t *testing.T) {
	pid := dropATemplate(t)
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("evaluator %d outlived its unreachable template", pid)
		}
		goruntime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}

const (
	silentLimit          = 16<<20 + 1
	foreignGreetingLimit = 16<<20 + 2
	futureProtocolLimit  = 16<<20 + 3
	malformedAnswerLimit = 16<<20 + 4
	emptyAnswerLimit     = 16<<20 + 5
	crashAtStartLimit    = 16<<20 + 6
)

var _ = impersonateAnEvaluator()

func impersonateAnEvaluator() bool {
	if os.Getenv(evaluatorMarker) != "1" {
		return false
	}
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(silentLimit), strconv.Itoa(foreignGreetingLimit), strconv.Itoa(futureProtocolLimit), strconv.Itoa(malformedAnswerLimit), strconv.Itoa(emptyAnswerLimit), strconv.Itoa(crashAtStartLimit):
	default:
		return false
	}
	requests := os.NewFile(requestsFD, "impostor-requests")
	replies := os.NewFile(responsesFD, "impostor-replies")
	greeting := hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: os.Getenv(evaluatorNonce)}
	switch os.Getenv(evaluatorMemoryLimit) {
	case strconv.Itoa(silentLimit):
	case strconv.Itoa(crashAtStartLimit):
		os.Exit(3)
	case strconv.Itoa(foreignGreetingLimit):
		_, _ = replies.WriteString("hello from some other program\n")
		os.Exit(0)
	case strconv.Itoa(futureProtocolLimit):
		greeting.Version = 999
		_ = json.NewEncoder(replies).Encode(greeting)
	case strconv.Itoa(malformedAnswerLimit):
		answerOnce(requests, replies, greeting, "{\"data\": nope}\n")
	case strconv.Itoa(emptyAnswerLimit):
		answerOnce(requests, replies, greeting, "{}\n")
	}
	_, _ = io.Copy(io.Discard, requests)
	os.Exit(0)
	return true
}

func answerOnce(requests, replies *os.File, greeting hello, reply string) {
	_ = json.NewEncoder(replies).Encode(greeting)
	incoming := json.NewDecoder(requests)
	var next json.RawMessage
	_ = incoming.Decode(&next)
	_, _ = replies.WriteString("null\n")
	_ = incoming.Decode(&next)
	_, _ = replies.WriteString(reply)
}

func TestAForeignExecutableIsUnsupported(t *testing.T) {
	_, err := MakeSecureTemplate(within(t, 10*time.Second), "v: ${ 1 }\n", WithMemoryLimit(foreignGreetingLimit))
	expectError(t, err, Unsupported, "is not a jaml evaluator")
}

func TestAReplacedExecutableIsUnsupported(t *testing.T) {
	_, err := MakeSecureTemplate(within(t, 10*time.Second), "v: ${ 1 }\n", WithMemoryLimit(futureProtocolLimit))
	expectError(t, err, Unsupported, "speaks jaml protocol v999, want v2")
}

func TestASilentExecutableEndsWithTheContext(t *testing.T) {
	started := time.Now()
	_, err := MakeSecureTemplate(within(t, 300*time.Millisecond), "v: ${ 1 }\n", WithMemoryLimit(silentLimit))
	expectError(t, err, EvaluationFailed, "starting the evaluator", "cancelled", "deadline exceeded")
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("a silent evaluator held construction for %s", waited)
	}
}

func TestAMalformedAnswerKillsTheEvaluatorWithItsCause(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n", WithMemoryLimit(malformedAnswerLimit))
	process := currentEvaluator(template)
	started := time.Now()
	expectError(t, evalBounded(t, template, within(t, 30*time.Second), "v"), EvaluationFailed, "$.v", "reading the evaluator's answer", "invalid character")
	if waited := time.Since(started); waited > 3*time.Second {
		t.Fatalf("a malformed answer was noticed only after %s", waited)
	}
	expectDeath(t, process)
}

func TestAnAnswerWithNeitherDataNorFailureKillsTheEvaluator(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n", WithMemoryLimit(emptyAnswerLimit))
	process := currentEvaluator(template)
	expectError(t, evalBounded(t, template, within(t, 30*time.Second), "v"), EvaluationFailed, "$.v", "neither data nor a failure")
	expectDeath(t, process)
}

func TestCloseDuringARespawnReturns(t *testing.T) {
	template := mustSecure(t, "v: ${ 1 }\n")
	slot := <-template.slot
	slot.spec.memoryLimit = silentLimit
	_ = slot.process.failWith(errors.New("killed by the test"))
	template.slot <- slot
	evaluating := make(chan error, 1)
	go func() {
		var out int
		evaluating <- template.Eval(context.Background(), &out, nil, "v")
	}()
	time.Sleep(200 * time.Millisecond)
	started := time.Now()
	_ = template.Close()
	if took := time.Since(started); took > 100*time.Millisecond {
		t.Fatalf("Close took %s behind a respawn", took)
	}
	select {
	case err := <-evaluating:
		expectError(t, err, Closed)
	case <-time.After(5 * time.Second):
		t.Fatal("the respawning Eval outlived Close")
	}
}

func evaluatorCPU(t *testing.T, idle time.Duration) time.Duration {
	template, err := MakeSecureTemplate(context.Background(), "v: ${ 1 }\n")
	if err != nil {
		t.Fatal(err)
	}
	process := currentEvaluator(template)
	time.Sleep(idle)
	_ = template.Close()
	expectDeath(t, process)
	return process.cmd.ProcessState.UserTime() + process.cmd.ProcessState.SystemTime()
}

func TestAnIdleEvaluatorSleeps(t *testing.T) {
	startup := evaluatorCPU(t, 0)
	idling := evaluatorCPU(t, 2*time.Second)
	if spent := idling - startup; spent > 15*time.Millisecond {
		t.Fatalf("an idle evaluator spent %s of CPU in 2s (startup alone %s)", spent, startup)
	}
}

func TestADeathNamesItsReasonNotItsStack(t *testing.T) {
	dump := "fatal error: runtime: out of memory\n\nruntime stack:\nruntime.throw({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:1101\n\ngoroutine 1 [running]:\nmain.main()\n"
	stderr := &headBuffer{limit: stderrHeadBytes}
	_, _ = stderr.Write([]byte("jaml evaluator: something first\n" + dump + strings.Repeat("goroutine 9 [chan receive]:\n", 1000)))
	expectEqual(t, stderr.reason(), "fatal error: runtime: out of memory")
	plain := &headBuffer{limit: stderrHeadBytes}
	_, _ = plain.Write([]byte("jaml evaluator: reading request: bad\n"))
	expectEqual(t, plain.reason(), "jaml evaluator: reading request: bad")
}

func TestARespawnFailsAsConstructionWouldHave(t *testing.T) {
	for name, limit := range map[string]int64{"foreign greeting": foreignGreetingLimit, "future protocol": futureProtocolLimit, "crash before greeting": crashAtStartLimit} {
		t.Run(name, func(t *testing.T) {
			_, constructed := MakeSecureTemplate(within(t, 10*time.Second), "a: 1\n", WithMemoryLimit(limit))
			template := mustSecure(t, "a: \"${ spin ? (() => { while (true) {} })() : 1 }\"\n")
			expectEqual(t, secureEvalAs[int](t, template, map[string]any{"spin": false}, "a"), 1)
			slot := <-template.slot
			slot.spec.memoryLimit = memoryLimit(limit)
			template.slot <- slot
			var out any
			expectError(t, template.Eval(within(t, 200*time.Millisecond), &out, map[string]any{"spin": true}, "a"), EvaluationFailed, "deadline exceeded")
			respawned := template.Eval(within(t, 10*time.Second), &out, map[string]any{"spin": false}, "a")
			expectError(t, respawned, Unsupported)
			expectEqual(t, respawned.Error(), constructed.Error())
			for _, kind := range everyKind {
				if errors.Is(respawned, kind) != errors.Is(constructed, kind) {
					t.Fatalf("a respawn and a construction disagree on %v:\nrespawn:      %v\nconstruction: %v", kind, respawned, constructed)
				}
			}
			if strings.Contains(respawned.Error(), "binding vars") {
				t.Fatalf("a respawn failure is blamed on the vars: %v", respawned)
			}
		})
	}
}

func liveChildren(t *testing.T) int {
	t.Helper()
	listed, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return len(strings.Fields(string(listed)))
}

func TestEvalsWithADoneContextStartNoEvaluator(t *testing.T) {
	template := mustSecure(t, "a: \"${ spin ? (() => { while (true) {} })() : 1 }\"\n")
	killed := currentEvaluator(template)
	var out any
	expectError(t, template.Eval(within(t, 100*time.Millisecond), &out, map[string]any{"spin": true}, "a"), EvaluationFailed, "deadline exceeded")
	expectDeath(t, killed)
	before := liveChildren(t)
	done, stop := context.WithCancel(context.Background())
	stop()
	for range 40 {
		err := template.Eval(done, &out, map[string]any{"spin": false}, "a")
		expectError(t, err, EvaluationFailed, "cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled in %v", err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if after := liveChildren(t); after > before {
		t.Fatalf("%d Evals with a done context left %d evaluators running", 40, after-before)
	}
	expectEqual(t, secureEvalAs[int](t, template, map[string]any{"spin": false}, "a"), 1)
}

func TestAHandshakeCutShortEndsItsEvaluator(t *testing.T) {
	before := liveChildren(t)
	for range 10 {
		_, err := MakeSecureTemplate(within(t, 50*time.Millisecond), "v: ${ 1 }\n", WithMemoryLimit(silentLimit))
		expectError(t, err, EvaluationFailed, "cancelled")
	}
	time.Sleep(100 * time.Millisecond)
	if after := liveChildren(t); after > before {
		t.Fatalf("cut-short handshakes left %d evaluators running", after-before)
	}
}
