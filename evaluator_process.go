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
	"sync/atomic"
	"time"
)

const (
	stderrHeadBytes    = 4096
	runtimeOutOfMemory = "fatal error: runtime: out of memory"
	cancelGrace        = 250 * time.Millisecond
)

var errHungUp = errors.New("the evaluator hung up")

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

type evaluatorProcess struct {
	cmd     *exec.Cmd
	dead    chan struct{}
	once    sync.Once
	cause   error
	line    *conversation
	catalog *catalog
}

type conversation struct {
	writing  sync.Mutex
	requests *os.File
	jobs     atomic.Uint64
	mu       sync.Mutex
	pending  map[uint64]chan reply
}

type catalog struct {
	buildTimeout time.Duration
	mu           sync.Mutex
	built        map[uint64]*building
}

type building struct {
	done   chan struct{}
	result outcome
}

type answer struct {
	at    string
	final reply
	died  bool
}

func spawnEvaluator(ctx context.Context, spec childSpec) (*evaluatorProcess, error) {
	if ctx.Err() != nil {
		return nil, startFailure(cancelled(ctx))
	}
	bounded, stop := context.WithTimeoutCause(ctx, spec.buildTimeout, fmt.Errorf("the evaluator took longer than %s to start: %w", spec.buildTimeout, context.DeadlineExceeded))
	defer stop()
	world := currentWorld(spec.limit)
	process, replies, err := world.start(spec)
	if err != nil {
		return nil, startFailure(err)
	}
	if err := process.greet(bounded, replies, world.nonce); err != nil {
		return nil, startFailure(process.abandon(err))
	}
	go process.listen(replies)
	return process, nil
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

func (w evaluatorWorld) start(spec childSpec) (*evaluatorProcess, *json.Decoder, error) {
	image, err := selfImage()
	if err != nil {
		return nil, nil, fmt.Errorf("finding this executable: %w", err)
	}
	requestsRead, requestsWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	repliesRead, repliesWrite, err := os.Pipe()
	if err != nil {
		closeAll(requestsRead, requestsWrite)
		return nil, nil, err
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
		return nil, nil, err
	}
	process := &evaluatorProcess{
		cmd:     cmd,
		dead:    make(chan struct{}),
		line:    &conversation{requests: requestsWrite, pending: map[uint64]chan reply{}},
		catalog: &catalog{buildTimeout: spec.buildTimeout, built: map[uint64]*building{}},
	}
	unbind := context.AfterFunc(spec.lifetime, func() { process.kill(fmt.Errorf("closed: %w", context.Cause(spec.lifetime))) })
	go process.reap(func() error {
		closeAll(requestsWrite, repliesRead)
		unbind()
		return w.describeExit(cmd.ProcessState, stderr)
	})
	return process, json.NewDecoder(repliesRead), nil
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
	p.kill(bury())
	close(p.dead)
}

func (p *evaluatorProcess) kill(cause error) {
	if cause != nil {
		p.once.Do(func() { p.cause = cause })
	}
	_ = p.cmd.Process.Kill()
}

func (p *evaluatorProcess) abandon(cause error) error {
	p.kill(cause)
	<-p.dead
	return cause
}

func (p *evaluatorProcess) deathCause() error {
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

func (p *evaluatorProcess) greet(ctx context.Context, replies *json.Decoder, nonce string) error {
	var greeting hello
	read := make(chan error, 1)
	go func() { read <- decodeReply(replies, &greeting) }()
	var err error
	select {
	case err = <-read:
	case <-ctx.Done():
		return cancelled(ctx)
	}
	switch {
	case err != nil:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): %w", err)
	case greeting.Magic != evaluatorMagic || greeting.Nonce != nonce:
		return fail(Unsupported, "this executable is not a jaml evaluator (C shared library or plugin host?): it answered with a foreign greeting")
	case greeting.Version != protocolVersion:
		return fail(Unsupported, "the evaluator speaks jaml protocol v%d, want v%d (executable replaced since start?)", greeting.Version, protocolVersion)
	}
	return nil
}

func (p *evaluatorProcess) listen(replies *json.Decoder) {
	for {
		var incoming reply
		if err := decodeReply(replies, &incoming); err != nil {
			if !errors.Is(err, errHungUp) {
				p.kill(err)
			}
			return
		}
		if err := incoming.malformed(); err != nil {
			p.kill(err)
			return
		}
		if err := p.line.deliver(incoming); err != nil {
			p.kill(err)
			return
		}
	}
}

func (p *evaluatorProcess) exchange(ctx context.Context, outgoing request) (answer, error) {
	job := p.line.jobs.Add(1)
	encoded, err := encodeRequest(outgoing.withJob(job))
	if err != nil {
		return answer{}, err
	}
	replies := p.line.register(job)
	defer p.line.close(job)
	if err := p.line.write(encoded); err != nil {
		p.kill(nil)
		<-p.dead
		return answer{died: true}, nil
	}
	return p.await(ctx, job, replies), nil
}

func (p *evaluatorProcess) await(ctx context.Context, job uint64, replies <-chan reply) answer {
	var at string
	var grace <-chan time.Time
	stopped := ctx.Done()
	for {
		select {
		case incoming := <-replies:
			if incoming.At == nil {
				return answer{at: at, final: incoming}
			}
			at = *incoming.At
		case <-p.dead:
			return answer{at: at, died: true}
		case <-stopped:
			stopped = nil
			go p.line.tell(request{Op: opCancel, Job: job})
			timer := time.NewTimer(cancelGrace)
			defer timer.Stop()
			grace = timer.C
		case <-grace:
			grace = nil
			p.kill(cancelled(ctx))
		}
	}
}

func (c *conversation) register(job uint64) <-chan reply {
	replies := make(chan reply, 2)
	c.mu.Lock()
	c.pending[job] = replies
	c.mu.Unlock()
	return replies
}

func (c *conversation) close(job uint64) {
	c.mu.Lock()
	delete(c.pending, job)
	c.mu.Unlock()
}

func (c *conversation) deliver(incoming reply) error {
	c.mu.Lock()
	waiting := c.pending[incoming.Job]
	c.mu.Unlock()
	if waiting == nil {
		return nil
	}
	select {
	case waiting <- incoming:
		return nil
	default:
		return fmt.Errorf("the evaluator answered job %d more than twice", incoming.Job)
	}
}

func (c *conversation) tell(outgoing request) {
	if encoded, err := encodeRequest(outgoing); err == nil {
		_ = c.write(encoded)
	}
}

func (c *conversation) write(encoded []byte) error {
	c.writing.Lock()
	defer c.writing.Unlock()
	if _, err := c.requests.Write(encoded); err != nil {
		return errHungUp
	}
	return nil
}

func (p *evaluatorProcess) ensureBuilt(ctx context.Context, unit unitRef) outcome {
	for {
		pending, claimed := p.catalog.claim(unit.id)
		if claimed {
			pending.result = p.build(ctx, unit)
			if pending.result.failed() {
				p.catalog.drop(unit.id)
			}
			close(pending.done)
			return pending.result
		}
		select {
		case <-pending.done:
			if !pending.result.failed() {
				return outcome{}
			}
		case <-p.dead:
			return outcome{died: true}
		case <-ctx.Done():
			return outcome{err: cancelled(ctx)}
		}
	}
}

func (p *evaluatorProcess) build(ctx context.Context, unit unitRef) outcome {
	bounded, stop := context.WithTimeoutCause(ctx, p.catalog.buildTimeout, fmt.Errorf("the build took longer than its timeout of %s: %w", p.catalog.buildTimeout, context.DeadlineExceeded))
	defer stop()
	plan := unit.plan
	answered, err := p.exchange(bounded, request{Op: opBuild, Unit: unit.id, Plan: &plan})
	switch {
	case err != nil:
		return outcome{err: err}
	case answered.died:
		return outcome{died: true, err: p.culprit(bounded)}
	case answered.final.Built && ctx.Err() != nil:
		p.forget(unit.id)
		return outcome{err: cancelled(ctx)}
	case answered.final.Built:
		return outcome{}
	case answered.final.Failure != nil:
		return outcome{err: answered.final.Failure.rebuild()}
	case bounded.Err() != nil:
		return outcome{err: cancelled(bounded)}
	}
	p.kill(errors.New("the evaluator answered a build with data"))
	return outcome{died: true}
}

func (p *evaluatorProcess) serve(ctx context.Context, outgoing request) outcome {
	answered, err := p.exchange(ctx, outgoing)
	switch {
	case err != nil:
		return outcome{err: err}
	case answered.died:
		return outcome{at: answered.at, died: true, err: p.culprit(ctx)}
	case answered.final.Failure != nil:
		return outcome{at: answered.at, err: answered.final.Failure.rebuild()}
	case len(answered.final.Data) != 0:
		return outcome{at: answered.at, data: answered.final.Data}
	}
	p.kill(errors.New("the evaluator answered an evaluation with a build"))
	return outcome{at: answered.at, died: true}
}

func (p *evaluatorProcess) culprit(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return p.deathCause()
}

func (p *evaluatorProcess) forget(id uint64) {
	if p.catalog.drop(id) {
		go p.line.tell(request{Op: opForget, Unit: id})
	}
}

func (c *catalog) claim(id uint64) (*building, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pending, found := c.built[id]; found {
		return pending, false
	}
	pending := &building{done: make(chan struct{})}
	c.built[id] = pending
	return pending, true
}

func (c *catalog) drop(id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, found := c.built[id]
	delete(c.built, id)
	return found
}

func (c *catalog) holds(id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, found := c.built[id]
	return found
}

func encodeRequest(outgoing any) ([]byte, error) {
	encoded, err := json.Marshal(outgoing)
	if err != nil {
		return nil, fmt.Errorf("writing the evaluator's request: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeReply(replies *json.Decoder, into any) error {
	err := replies.Decode(into)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, os.ErrClosed):
		return errHungUp
	default:
		return fmt.Errorf("reading the evaluator's answer: %w", err)
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
	lines := strings.Split(string(bytes.TrimSpace(h.kept)), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
			return strings.TrimSpace(line)
		}
	}
	return strings.TrimSpace(lines[0])
}
