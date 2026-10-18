package jaml

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stderrHeadBytes    = 4096
	runtimeOutOfMemory = "fatal error: runtime: out of memory"
)

var errHungUp = errors.New("the evaluator hung up")

type evaluatorSpec struct {
	blueprint   blueprint
	memoryLimit memoryLimit
}

type evaluatorWorld struct {
	limit  memoryLimit
	nonce  string
	parent int
	zone   []string
}

type evaluatorProcess struct {
	cmd      *exec.Cmd
	requests *os.File
	replies  *json.Decoder
	dead     chan struct{}
	once     sync.Once
	cause    error
}

func spawnEvaluator(ctx, lifetime context.Context, spec evaluatorSpec) (*evaluatorProcess, error) {
	if ctx.Err() != nil {
		return nil, cancelled(ctx)
	}
	world := currentWorld(spec.memoryLimit)
	process, err := world.start(lifetime)
	if err != nil {
		return nil, err
	}
	if err := process.handshake(ctx, world.nonce, spec.blueprint); err != nil {
		return nil, process.abandon(err)
	}
	return process, nil
}

func (p *evaluatorProcess) handshake(ctx context.Context, nonce string, plan blueprint) error {
	if err := p.greet(ctx, nonce); err != nil {
		return err
	}
	encoded, err := encodeRequest(plan)
	if err != nil {
		return err
	}
	var refused *failure
	if err := p.within(ctx, func() error { return p.ask(encoded, &refused) }); err != nil {
		return err
	}
	if refused != nil {
		return refused.rebuild()
	}
	return nil
}

func currentWorld(limit memoryLimit) evaluatorWorld {
	return evaluatorWorld{limit: limit, nonce: rand.Text(), parent: os.Getpid(), zone: zoneEnv()}
}

func zoneEnv() []string {
	var env []string
	if zoneinfo := os.Getenv("ZONEINFO"); zoneinfo != "" {
		env = append(env, "ZONEINFO="+zoneinfo)
	}
	if name := forwardableZone(); name != "" {
		env = append(env, "TZ="+name)
	}
	return env
}

func forwardableZone() string {
	name := time.Local.String()
	switch {
	case name == "Local":
		return ""
	case filepath.IsAbs(name):
		if info, err := os.Stat(name); err != nil || !info.Mode().IsRegular() {
			return ""
		}
		return name
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

func (w evaluatorWorld) env() []string {
	env := []string{
		evaluatorMarker + "=1",
		evaluatorMemoryLimit + "=" + strconv.FormatInt(int64(w.limit), 10),
		evaluatorNonce + "=" + w.nonce,
		evaluatorParent + "=" + strconv.Itoa(w.parent),
	}
	return append(env, w.zone...)
}

func (w evaluatorWorld) start(lifetime context.Context) (*evaluatorProcess, error) {
	image, err := selfImage()
	if err != nil {
		return nil, fmt.Errorf("finding this executable: %w", err)
	}
	requestsRead, requestsWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	repliesRead, repliesWrite, err := os.Pipe()
	if err != nil {
		closeAll(requestsRead, requestsWrite)
		return nil, err
	}
	stderr := &headBuffer{limit: stderrHeadBytes}
	cmd := exec.Command(image)
	cmd.Env = w.env()
	cmd.ExtraFiles = []*os.File{requestsRead, repliesWrite}
	cmd.Stderr = stderr
	cmd.SysProcAttr = evaluatorProcAttr()
	err = cmd.Start()
	closeAll(requestsRead, repliesWrite)
	if err != nil {
		closeAll(requestsWrite, repliesRead)
		return nil, err
	}
	process := &evaluatorProcess{cmd: cmd, requests: requestsWrite, replies: json.NewDecoder(repliesRead), dead: make(chan struct{})}
	unbind := context.AfterFunc(lifetime, func() { process.die(fmt.Errorf("template closed: %w", context.Cause(lifetime))) })
	go process.reap(func() error {
		closeAll(requestsWrite, repliesRead)
		unbind()
		return w.describeExit(cmd.ProcessState, stderr)
	})
	return process, nil
}

func (w evaluatorWorld) describeExit(state *os.ProcessState, stderr *headBuffer) error {
	reason := stderr.reason()
	switch {
	case state.ExitCode() == exitMemoryLimit:
		return memoryExhausted(fmt.Sprintf("exceeded the memory limit of %d bytes", w.limit))
	case reason == runtimeOutOfMemory:
		return memoryExhausted(fmt.Sprintf("evaluator died (%s): %s", state, reason))
	case reason != "":
		return fmt.Errorf("evaluator died (%s): %s", state, reason)
	}
	return fmt.Errorf("evaluator died (%s)", state)
}

func (p *evaluatorProcess) reap(bury func() error) {
	_ = p.cmd.Wait()
	p.die(bury())
	close(p.dead)
}

func (p *evaluatorProcess) die(cause error) {
	if cause != nil {
		p.once.Do(func() { p.cause = cause })
	}
	_ = p.cmd.Process.Kill()
}

func (p *evaluatorProcess) failWith(cause error) error {
	p.die(cause)
	<-p.dead
	return p.cause
}

func (p *evaluatorProcess) abandon(cause error) error {
	p.die(cause)
	<-p.dead
	return cause
}

func (p *evaluatorProcess) alive() bool {
	select {
	case <-p.dead:
		return false
	default:
		return true
	}
}

func (p *evaluatorProcess) greet(ctx context.Context, nonce string) error {
	var greeting hello
	err := p.within(ctx, func() error { return p.read(&greeting) })
	switch {
	case err != nil && ctx.Err() != nil:
		return err
	case err != nil:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): %w", err)
	case greeting.Magic != evaluatorMagic || greeting.Nonce != nonce:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): it answered with a foreign greeting")
	case greeting.Version != protocolVersion:
		return fail(Unsupported, "the evaluator speaks jaml protocol v%d, want v%d (executable replaced since start?)", greeting.Version, protocolVersion)
	}
	return nil
}

func (p *evaluatorProcess) render(ctx context.Context, job rendering) (string, []byte, error) {
	encoded, err := encodeRequest(job)
	if err != nil {
		return "", nil, located("", err)
	}
	var choice selection
	var reply rendered
	err = p.within(ctx, func() error {
		if err := p.ask(encoded, &choice); err != nil {
			return err
		}
		if err := choice.malformed(); err != nil || choice.Failure != nil {
			return err
		}
		return p.read(&reply)
	})
	switch {
	case err != nil && choice.malformed() != nil:
		return "", nil, located("", err)
	case err != nil:
		return "", nil, located(choice.At, err)
	case choice.Failure != nil:
		return "", nil, choice.Failure.rebuild()
	}
	if err := reply.malformed(); err != nil {
		return "", nil, located(choice.At, p.failWith(err))
	}
	if reply.Failure != nil {
		return "", nil, reply.Failure.rebuild()
	}
	return choice.At, reply.Data, nil
}

func encodeRequest(outgoing any) ([]byte, error) {
	encoded, err := json.Marshal(outgoing)
	if err != nil {
		return nil, fmt.Errorf("writing the evaluator's request: %w", err)
	}
	return append(encoded, '\n'), nil
}

func (p *evaluatorProcess) ask(encoded []byte, incoming any) error {
	if _, err := p.requests.Write(encoded); err != nil {
		return errHungUp
	}
	return p.read(incoming)
}

func located(at string, err error) error {
	if _, isLocated := err.(templateError); isLocated {
		return err
	}
	if at == "" {
		return fail(EvaluationFailed, "%w", err)
	}
	return fail(EvaluationFailed, "%s: %w", at, err)
}

func (p *evaluatorProcess) within(ctx context.Context, exchange func() error) error {
	if ctx.Err() != nil {
		return cancelled(ctx)
	}
	stop := context.AfterFunc(ctx, func() { p.die(cancelled(ctx)) })
	err := exchange()
	stopped := stop()
	switch {
	case !stopped:
		return p.failWith(cancelled(ctx))
	case err == nil:
		return nil
	case errors.Is(err, errHungUp):
		return p.failWith(nil)
	default:
		return p.failWith(err)
	}
}

func (p *evaluatorProcess) read(into any) error {
	err := p.replies.Decode(into)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, os.ErrClosed):
		return errHungUp
	default:
		return fmt.Errorf("reading the evaluator's answer: %w", err)
	}
}

func cancelled(ctx context.Context) error {
	return fmt.Errorf("cancelled: %w", context.Cause(ctx))
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

type headBuffer struct {
	limit int
	mu    sync.Mutex
	kept  []byte
}

func (h *headBuffer) Write(written []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := max(h.limit-len(h.kept), 0)
	h.kept = append(h.kept, written[:min(room, len(written))]...)
	return len(written), nil
}

func (h *headBuffer) reason() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	lines := strings.Split(string(bytes.TrimSpace(h.kept)), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
			return strings.TrimSpace(line)
		}
	}
	return strings.TrimSpace(lines[0])
}
