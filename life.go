package jaml

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	stderrHeadBytes        = 4096
	runtimeOutOfMemory     = "fatal error: runtime: out of memory"
	pressureMarker         = "jaml evaluator: the live heap passed three quarters of its ceiling"
	cancelGrace            = 250 * time.Millisecond
	backstopSilence        = 20 * cancelGrace
	evaluatorStartDeadline = 30 * time.Second
)

var (
	errHungUp     = errors.New("the evaluator hung up")
	errRetired    = errors.New("retired after replaying one job alone")
	errSilent     = fmt.Errorf("the evaluator stopped answering for %s after it was told to stop a job", backstopSilence)
	errNotReading = fmt.Errorf("the evaluator was not reading requests for %s after a caller's context ended", backstopSilence)
)

type _EvaluatorWorld struct {
	limit  _MemoryLimit
	nonce  string
	parent int
	zone   []string
}

type _ChildSpec struct {
	lifetime      context.Context
	limit         _MemoryLimit
	startDeadline time.Duration
	buildTimeout  time.Duration
}

type _Life struct {
	cmd          *exec.Cmd
	signals      sync.Mutex
	reaped       chan struct{}
	drained      chan struct{}
	ended        chan struct{}
	verdict      error
	killCause    atomic.Pointer[error]
	buildTimeout time.Duration
	wire         *_Wire
	board        *_Switchboard
	quiet        *_Quiet
}

type _Ending interface {
	verdict(remains _Remains) error
}

type _Greeting struct {
	hello _Hello
	err   error
}

type _Collected struct {
	state *os.ProcessState
}

type _Uncollected struct {
	waited error
}

type _Remains struct {
	stderr    *_HeadBuffer
	killCause error
	limit     _MemoryLimit
}

func endingOf(cmd *exec.Cmd, waited error) _Ending {
	if cmd.ProcessState == nil {
		return _Uncollected{waited: waited}
	}
	return _Collected{state: cmd.ProcessState}
}

func startLife(spec _ChildSpec) (*_Life, error) {
	bounded, stop := context.WithTimeoutCause(spec.lifetime, spec.startDeadline, fmt.Errorf("the evaluator took longer than %s to start: %w", spec.startDeadline, context.DeadlineExceeded))
	defer stop()
	if bounded.Err() != nil {
		return nil, startFailure(cancelled(bounded))
	}
	var world = currentWorld(spec.limit)
	born, greeted, err := world.start(spec)
	if err != nil {
		return nil, startFailure(err)
	}
	greeting, err := born.greet(bounded, greeted)
	if err == nil {
		err = greeting.vouch(world.nonce)
	}
	if err != nil {
		return nil, startFailure(born.abandon(spec.lifetime, err))
	}
	return born, nil
}

func startFailure(err error) error {
	if _, isOwn := err.(_ExpressionError); isOwn {
		return err
	}
	return fail(EvaluationFailed, "starting the evaluator: %w", err)
}

func currentWorld(limit _MemoryLimit) _EvaluatorWorld {
	return _EvaluatorWorld{limit: limit, nonce: rand.Text(), parent: os.Getpid(), zone: zoneEnv()}
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
	var name = time.Local.String()
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

func (w _EvaluatorWorld) env() []string {
	var env = []string{
		evaluatorMarker + "=1",
		evaluatorMemoryLimit + "=" + strconv.FormatInt(int64(w.limit), 10),
		evaluatorNonce + "=" + w.nonce,
		evaluatorParent + "=" + strconv.Itoa(w.parent),
	}
	return append(env, w.zone...)
}

func (w _EvaluatorWorld) start(spec _ChildSpec) (*_Life, <-chan _Greeting, error) {
	image, err := selfImage()
	if err != nil {
		return nil, nil, fmt.Errorf("finding this executable: %w", err)
	}
	pipes, err := openPipes(3)
	if err != nil {
		return nil, nil, err
	}
	requests, replies, control := pipes[0], pipes[1], pipes[2]
	var stderr = &_HeadBuffer{limit: stderrHeadBytes}
	var cmd = exec.Command(image)
	cmd.Env = w.env()
	cmd.ExtraFiles = []*os.File{requests.reader, replies.writer, control.reader}
	cmd.Stderr = stderr
	cmd.SysProcAttr = evaluatorProcAttr()
	cmd.WaitDelay = cancelGrace
	err = cmd.Start()
	closeAll(requests.reader, replies.writer, control.reader)
	if err != nil {
		closeAll(requests.writer, replies.reader, control.writer)
		return nil, nil, err
	}
	var born = &_Life{
		cmd:          cmd,
		reaped:       make(chan struct{}),
		drained:      make(chan struct{}),
		ended:        make(chan struct{}),
		buildTimeout: spec.buildTimeout,
		wire:         newWire(requests.writer, control.writer),
		board:        &_Switchboard{pending: map[uint64]*_Expectation{}},
		quiet:        &_Quiet{meaning: errSilent},
	}
	var unbind = context.AfterFunc(spec.lifetime, func() { born.kill(fmt.Errorf("closed: %w", context.Cause(spec.lifetime))) })
	var greeted = make(chan _Greeting, 1)
	go born.listen(&_ReplyStream{frames: framesFor(w.limit).replies(replies.reader, born.quiet)}, greeted)
	go func() {
		var ending = endingOf(cmd, cmd.Wait())
		born.reap()
		var remains = _Remains{stderr: stderr, killCause: born.killReason(), limit: w.limit}
		closeAll(requests.writer, control.writer)
		unbind()
		born.drain(replies.reader)
		born.bury(ending.verdict(remains))
	}()
	return born, greeted, nil
}

type _Pipe struct {
	reader *os.File
	writer *os.File
}

func openPipes(count int) ([]_Pipe, error) {
	var opened []_Pipe
	for range count {
		reader, writer, err := os.Pipe()
		if err != nil {
			for _, made := range opened {
				closeAll(made.reader, made.writer)
			}
			return nil, err
		}
		opened = append(opened, _Pipe{reader: reader, writer: writer})
	}
	return opened, nil
}

func (l *_Life) bury(verdict error) {
	l.verdict = verdict
	close(l.ended)
	l.wire.vacate()
}

func (l *_Life) kill(reason error) {
	l.killCause.CompareAndSwap(nil, &reason)
	l.end()
}

func (l *_Life) end() {
	l.signals.Lock()
	defer l.signals.Unlock()
	select {
	case <-l.reaped:
	default:
		killGroup(l.cmd.Process)
	}
}

func (l *_Life) reap() {
	l.signals.Lock()
	defer l.signals.Unlock()
	close(l.reaped)
}

func (l *_Life) killReason() error {
	if reason := l.killCause.Load(); reason != nil {
		return *reason
	}
	return nil
}

func (l *_Life) drain(replies *os.File) {
	var bound = time.NewTimer(cancelGrace)
	defer bound.Stop()
	select {
	case <-l.drained:
	case <-bound.C:
	}
	_ = replies.Close()
	<-l.drained
}

func (l *_Life) abandon(ctx context.Context, reason error) error {
	l.kill(reason)
	select {
	case <-l.ended:
	case <-ctx.Done():
	}
	return reason
}

func (l *_Life) alive() bool {
	select {
	case <-l.ended:
		return false
	default:
		return true
	}
}

func (l *_Life) lost() _Lost {
	<-l.ended
	return _Lost{cause: l.verdict}
}

func (l *_Life) greet(ctx context.Context, greeted <-chan _Greeting) (_Hello, error) {
	select {
	case greeting := <-greeted:
		if errors.Is(greeting.err, errHungUp) {
			return greeting.hello, l.deathBeforeGreeting(ctx)
		}
		if greeting.err != nil {
			return greeting.hello, fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): %w", greeting.err)
		}
		return greeting.hello, nil
	case <-ctx.Done():
		return _Hello{}, cancelled(ctx)
	}
}

func (l *_Life) deathBeforeGreeting(ctx context.Context) error {
	select {
	case <-l.ended:
		return l.verdict
	case <-ctx.Done():
		return cancelled(ctx)
	}
}

func (greeting _Hello) vouch(nonce string) error {
	switch {
	case greeting.Magic != evaluatorMagic || greeting.Nonce != nonce:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): it answered with a foreign greeting")
	case greeting.Version != protocolVersion:
		return fail(Unsupported, "the evaluator speaks jaml protocol v%d, want v%d (executable replaced since start?)", greeting.Version, protocolVersion)
	}
	return nil
}

func (c _Collected) verdict(r _Remains) error {
	var reason = r.stderr.reason()
	switch {
	case c.state.ExitCode() == exitMemoryLimit:
		return _MemoryExhausted(fmt.Sprintf("exceeded the memory limit of %d bytes", r.limit))
	case c.state.ExitCode() == exitStuckAfterCancel:
		return fmt.Errorf("evaluator exited (%s): a job stuck after cancel was still inside a built-in %s after it was told to stop", c.state, cancelGrace)
	case reason == runtimeOutOfMemory:
		return _MemoryExhausted(fmt.Sprintf("evaluator died (%s): %s", c.state, reason))
	case killedOutright(c.state) && r.killCause != nil:
		return r.killCause
	case killedOutright(c.state) && r.stderr.holds(pressureMarker):
		return _MemoryExhausted(fmt.Sprintf("evaluator died (%s) from outside jaml near the memory limit of %d bytes, most likely by the kernel's out-of-memory killer", c.state, r.limit))
	case killedOutright(c.state):
		return fmt.Errorf("evaluator died (%s): killed from outside jaml, often by the kernel's out-of-memory killer%s", c.state, peakNote(c.state))
	case reason != "":
		return fmt.Errorf("evaluator died (%s): %s", c.state, reason)
	}
	return fmt.Errorf("evaluator died (%s)", c.state)
}

func (u _Uncollected) verdict(r _Remains) error {
	var reason = r.stderr.reason()
	switch {
	case reason == runtimeOutOfMemory:
		return _MemoryExhausted(fmt.Sprintf("evaluator ended but its exit could not be collected (%v): %s", u.waited, reason))
	case r.killCause != nil:
		return r.killCause
	case reason != "":
		return fmt.Errorf("evaluator ended but its exit could not be collected (%w): %s", u.waited, reason)
	case r.stderr.holds(pressureMarker):
		return _MemoryExhausted(fmt.Sprintf("evaluator ended near the memory limit of %d bytes but its exit could not be collected: %v", r.limit, u.waited))
	}
	return fmt.Errorf("evaluator ended but its exit could not be collected: %w", u.waited)
}

func peakNote(state *os.ProcessState) string {
	peak, known := peakResident(state)
	if !known {
		return ""
	}
	return fmt.Sprintf(", after peaking at %d MB resident", peak>>20)
}

type _ReplyStream struct {
	frames *_FrameReader
}

type _Quiet struct {
	since   atomic.Int64
	meaning error
}

var monotonicOrigin = time.Now()

func monotonicNow() time.Duration {
	return time.Since(monotonicOrigin)
}

func (q *_Quiet) waiting() {
	q.since.Store(int64(monotonicNow()) + 1)
}

func (q *_Quiet) heard() {
	q.since.Store(0)
}

func (q *_Quiet) lasted() time.Duration {
	var since = q.since.Load()
	if since == 0 {
		return 0
	}
	return monotonicNow() - time.Duration(since-1)
}

func (s *_ReplyStream) read(target any) error {
	frame, err := s.frames.next()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(frame, target); err != nil {
		return fmt.Errorf("reading the evaluator's answer: %w", err)
	}
	return nil
}

func located(path string, err error) error {
	if own, isOwn := err.(_ExpressionError); isOwn {
		return own
	}
	return fail(EvaluationFailed, "%w", err).within(path)
}

func cancelled(ctx context.Context) error {
	return fmt.Errorf("cancelled: %w", context.Cause(ctx))
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

type _HeadBuffer struct {
	limit int
	mu    sync.Mutex
	kept  []byte
}

func (h *_HeadBuffer) Write(written []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var room = max(h.limit-len(h.kept), 0)
	h.kept = append(h.kept, written[:min(room, len(written))]...)
	return len(written), nil
}

func (h *_HeadBuffer) reason() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var lines = slices.DeleteFunc(strings.Split(string(bytes.TrimSpace(h.kept)), "\n"), func(line string) bool { return line == pressureMarker })
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

func (h *_HeadBuffer) holds(marker string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Contains(h.kept, []byte(marker))
}
