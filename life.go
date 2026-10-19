package jaml

import (
	"bufio"
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	stderrHeadBytes    = 4096
	runtimeOutOfMemory = "fatal error: runtime: out of memory"
	pressureMarker     = "jaml evaluator: the live heap passed three quarters of its ceiling"
	cancelGrace        = 250 * time.Millisecond
)

var (
	errHungUp  = errors.New("the evaluator hung up")
	errRetired = errors.New("retired after replaying one job alone")
)

type evaluatorWorld struct {
	limit  memoryLimit
	nonce  string
	parent int
	zone   []string
}

type childSpec struct {
	lifetime     context.Context
	limit        memoryLimit
	buildTimeout time.Duration
}

type life struct {
	cmd          *exec.Cmd
	ended        chan struct{}
	verdict      error
	killedFor    atomic.Pointer[error]
	buildTimeout time.Duration
	wire         *wire
	board        *switchboard
}

type exit struct {
	state     *os.ProcessState
	stderr    *headBuffer
	killedFor error
	limit     memoryLimit
}

func startLife(spec childSpec) (*life, error) {
	bounded, stop := context.WithTimeoutCause(spec.lifetime, spec.buildTimeout, fmt.Errorf("the evaluator took longer than %s to start: %w", spec.buildTimeout, context.DeadlineExceeded))
	defer stop()
	if bounded.Err() != nil {
		return nil, startFailure(cancelled(bounded))
	}
	world := currentWorld(spec.limit)
	born, replies, err := world.start(spec)
	if err != nil {
		return nil, startFailure(err)
	}
	greeting, err := born.greet(bounded, replies)
	if err == nil {
		err = greeting.vouch(world.nonce)
	}
	if err != nil {
		return nil, startFailure(born.abandon(err))
	}
	go born.listen(replies)
	return born, nil
}

func startFailure(err error) error {
	if _, isTemplateError := err.(templateError); isTemplateError {
		return err
	}
	return fail(EvaluationFailed, "starting the evaluator: %w", err)
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

func (w evaluatorWorld) start(spec childSpec) (*life, *replyStream, error) {
	image, err := selfImage()
	if err != nil {
		return nil, nil, fmt.Errorf("finding this executable: %w", err)
	}
	pipes, err := openPipes(3)
	if err != nil {
		return nil, nil, err
	}
	requests, replies, control := pipes[0], pipes[1], pipes[2]
	stderr := &headBuffer{limit: stderrHeadBytes}
	cmd := exec.Command(image)
	cmd.Env = w.env()
	cmd.ExtraFiles = []*os.File{requests.read, replies.write, control.read}
	cmd.Stderr = stderr
	cmd.SysProcAttr = evaluatorProcAttr()
	err = cmd.Start()
	closeAll(requests.read, replies.write, control.read)
	if err != nil {
		closeAll(requests.write, replies.read, control.write)
		return nil, nil, err
	}
	born := &life{
		cmd:          cmd,
		ended:        make(chan struct{}),
		buildTimeout: spec.buildTimeout,
		wire:         newWire(requests.write, control.write),
		board:        &switchboard{pending: map[uint64]*expectation{}},
	}
	unbind := context.AfterFunc(spec.lifetime, func() { born.kill(fmt.Errorf("closed: %w", context.Cause(spec.lifetime))) })
	go func() {
		_ = cmd.Wait()
		killedFor := born.killReason()
		closeAll(requests.write, replies.read, control.write)
		unbind()
		born.bury(exit{state: cmd.ProcessState, stderr: stderr, killedFor: killedFor, limit: w.limit})
	}()
	return born, &replyStream{lines: bufio.NewReader(replies.read), frames: framesFor(w.limit)}, nil
}

type pipe struct {
	read  *os.File
	write *os.File
}

func openPipes(count int) ([]pipe, error) {
	var opened []pipe
	for range count {
		read, write, err := os.Pipe()
		if err != nil {
			for _, made := range opened {
				closeAll(made.read, made.write)
			}
			return nil, err
		}
		opened = append(opened, pipe{read: read, write: write})
	}
	return opened, nil
}

func (l *life) bury(ending exit) {
	l.verdict = ending.verdict()
	close(l.ended)
	l.wire.vacate()
}

func (l *life) kill(reason error) {
	l.killedFor.CompareAndSwap(nil, &reason)
	l.end()
}

func (l *life) end() {
	_ = l.cmd.Process.Kill()
}

func (l *life) killReason() error {
	if reason := l.killedFor.Load(); reason != nil {
		return *reason
	}
	return nil
}

func (l *life) abandon(reason error) error {
	l.kill(reason)
	<-l.ended
	return reason
}

func (l *life) alive() bool {
	select {
	case <-l.ended:
		return false
	default:
		return true
	}
}

func (l *life) lost() lost {
	<-l.ended
	return lost{cause: l.verdict}
}

func (l *life) greet(ctx context.Context, replies *replyStream) (hello, error) {
	var greeting hello
	read := make(chan error, 1)
	go func() { read <- replies.read(&greeting) }()
	select {
	case err := <-read:
		if errors.Is(err, errHungUp) {
			return greeting, l.deathBeforeGreeting(ctx)
		}
		if err != nil {
			return greeting, fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): %w", err)
		}
		return greeting, nil
	case <-ctx.Done():
		return greeting, cancelled(ctx)
	}
}

func (l *life) deathBeforeGreeting(ctx context.Context) error {
	select {
	case <-l.ended:
		return l.verdict
	case <-ctx.Done():
		return cancelled(ctx)
	}
}

func (greeting hello) vouch(nonce string) error {
	switch {
	case greeting.Magic != evaluatorMagic || greeting.Nonce != nonce:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): it answered with a foreign greeting")
	case greeting.Version != protocolVersion:
		return fail(Unsupported, "the evaluator speaks jaml protocol v%d, want v%d (executable replaced since start?)", greeting.Version, protocolVersion)
	}
	return nil
}

func (x exit) verdict() error {
	reason := x.stderr.reason()
	switch {
	case x.state.ExitCode() == exitMemoryLimit:
		return memoryExhausted(fmt.Sprintf("exceeded the memory limit of %d bytes", x.limit))
	case reason == runtimeOutOfMemory:
		return memoryExhausted(fmt.Sprintf("evaluator died (%s): %s", x.state, reason))
	case killedOutright(x.state) && x.killedFor != nil:
		return x.killedFor
	case killedOutright(x.state) && x.stderr.holds(pressureMarker):
		return memoryExhausted(fmt.Sprintf("evaluator died (%s) from outside jaml near the memory limit of %d bytes, most likely by the kernel's out-of-memory killer", x.state, x.limit))
	case killedOutright(x.state):
		return fmt.Errorf("evaluator died (%s): killed from outside jaml, often by the kernel's out-of-memory killer%s", x.state, peakNote(x.state))
	case reason != "":
		return fmt.Errorf("evaluator died (%s): %s", x.state, reason)
	}
	return fmt.Errorf("evaluator died (%s)", x.state)
}

func peakNote(state *os.ProcessState) string {
	peak, known := peakResident(state)
	if !known {
		return ""
	}
	return fmt.Sprintf(", after peaking at %d MB resident", peak>>20)
}

type replyStream struct {
	lines  *bufio.Reader
	frames frames
}

func (s *replyStream) read(into any) error {
	frame, err := s.frame()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(frame, into); err != nil {
		return fmt.Errorf("reading the evaluator's answer: %w", err)
	}
	return nil
}

func (s *replyStream) frame() ([]byte, error) {
	var frame []byte
	for {
		chunk, err := s.lines.ReadSlice('\n')
		if len(frame)+len(chunk) > s.frames.limit+1 {
			return nil, s.frames.oversized()
		}
		frame = append(frame, chunk...)
		switch {
		case err == nil:
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF), errors.Is(err, os.ErrClosed):
			return nil, errHungUp
		default:
			return nil, fmt.Errorf("reading the evaluator's answer: %w", err)
		}
	}
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
	lines := slices.DeleteFunc(strings.Split(string(bytes.TrimSpace(h.kept)), "\n"), func(line string) bool { return line == pressureMarker })
	for _, line := range lines {
		if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
			return strings.TrimSpace(line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func (h *headBuffer) holds(marker string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Contains(h.kept, []byte(marker))
}
