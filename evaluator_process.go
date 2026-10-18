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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gleno/fault"
)

const stderrHeadBytes = 4096

var errHungUp = errors.New("the evaluator hung up")

type evaluatorSpec struct {
	sources     []source
	memoryLimit memoryLimit
}

type evaluatorWorld struct {
	limit  memoryLimit
	nonce  string
	parent int
	zone   string
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
	world := currentWorld(spec.memoryLimit)
	process, err := world.start(lifetime)
	if err != nil {
		return nil, err
	}
	if err := process.greet(ctx, world.nonce); err != nil {
		return nil, err
	}
	answer, err := process.roundTrip(ctx, request{Op: opCompile, Sources: spec.sources})
	if err != nil {
		return nil, err
	}
	if answer.Error != "" {
		return nil, process.failWith(errors.New(answer.Error))
	}
	return process, nil
}

func currentWorld(limit memoryLimit) evaluatorWorld {
	return evaluatorWorld{limit: limit, nonce: rand.Text(), parent: os.Getpid(), zone: forwardableZone()}
}

func forwardableZone() string {
	name := time.Local.String()
	if name == "Local" {
		return ""
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
	if w.zone != "" {
		env = append(env, "TZ="+w.zone)
	}
	return env
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
	if state.ExitCode() == exitMemoryLimit {
		return fmt.Errorf("exceeded the memory limit of %d bytes", w.limit)
	}
	if reason := stderr.reason(); reason != "" {
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
		return fault.From(Unsupported, fmt.Sprintf("this executable is not a jaml evaluator (C shared library or plugin host?): %s", err))
	case greeting.Magic != evaluatorMagic || greeting.Nonce != nonce:
		return p.failWith(fault.From(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): it answered with a foreign greeting"))
	case greeting.Version != protocolVersion:
		return p.failWith(fault.From(Unsupported, fmt.Sprintf("the evaluator speaks jaml protocol v%d, want v%d (executable replaced since start?)", greeting.Version, protocolVersion)))
	}
	return nil
}

func (p *evaluatorProcess) roundTrip(ctx context.Context, next request) (response, error) {
	encoded, err := json.Marshal(next)
	if err != nil {
		return response{}, fmt.Errorf("writing the evaluator's request: %w", err)
	}
	var answer response
	err = p.within(ctx, func() error {
		if _, err := p.requests.Write(append(encoded, '\n')); err != nil {
			return errHungUp
		}
		return p.read(&answer)
	})
	return answer, err
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
