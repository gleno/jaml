package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

const stderrTailBytes = 4096

type evaluatorSpec struct {
	sources     []source
	memoryLimit int64
}

type evaluatorProcess struct {
	cmd       *exec.Cmd
	requests  *json.Encoder
	responses chan response
	exited    chan struct{}
	stderr    *tailBuffer
	limit     int64
}

func startEvaluator(ctx context.Context, spec evaluatorSpec) (*evaluatorProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding this executable to start the evaluator: %w", err)
	}
	requestsRead, requestsWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	responsesRead, responsesWrite, err := os.Pipe()
	if err != nil {
		closeAll(requestsRead, requestsWrite)
		return nil, err
	}
	process := &evaluatorProcess{
		cmd:       exec.Command(self),
		requests:  json.NewEncoder(requestsWrite),
		responses: make(chan response),
		exited:    make(chan struct{}),
		stderr:    &tailBuffer{limit: stderrTailBytes},
		limit:     spec.memoryLimit,
	}
	process.cmd.Env = []string{evaluatorMarker + "=1", evaluatorMemoryLimit + "=" + strconv.FormatInt(spec.memoryLimit, 10)}
	process.cmd.ExtraFiles = []*os.File{requestsRead, responsesWrite}
	process.cmd.Stderr = process.stderr
	process.cmd.SysProcAttr = evaluatorProcAttr()
	err = process.cmd.Start()
	closeAll(requestsRead, responsesWrite)
	if err != nil {
		closeAll(requestsWrite, responsesRead)
		return nil, fmt.Errorf("starting the evaluator: %w", err)
	}
	go process.readResponses(responsesRead)
	go process.wait(requestsWrite)
	answer, err := process.roundTrip(ctx, request{Op: opCompile, Sources: spec.sources})
	if err != nil {
		return nil, err
	}
	if answer.Error != "" {
		process.kill()
		return nil, errors.New(answer.Error)
	}
	return process, nil
}

func (p *evaluatorProcess) readResponses(from *os.File) {
	defer close(p.responses)
	defer closeAll(from)
	decoder := json.NewDecoder(from)
	for {
		var next response
		if err := decoder.Decode(&next); err != nil {
			return
		}
		select {
		case p.responses <- next:
		case <-p.exited:
			return
		}
	}
}

func (p *evaluatorProcess) wait(requests *os.File) {
	_ = p.cmd.Wait()
	closeAll(requests)
	close(p.exited)
}

func (p *evaluatorProcess) roundTrip(ctx context.Context, next request) (response, error) {
	if ctx.Err() != nil {
		p.kill()
		return response{}, fmt.Errorf("cancelled: %w", context.Cause(ctx))
	}
	if p.hasExited() {
		return response{}, p.failure()
	}
	if err := p.requests.Encode(next); err != nil {
		p.kill()
		return response{}, p.failure()
	}
	select {
	case answer, open := <-p.responses:
		if !open {
			return response{}, p.failure()
		}
		return answer, nil
	case <-ctx.Done():
		p.kill()
		return response{}, fmt.Errorf("cancelled: %w", context.Cause(ctx))
	}
}

func (p *evaluatorProcess) hasExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *evaluatorProcess) kill() {
	if !p.hasExited() {
		_ = p.cmd.Process.Kill()
	}
	<-p.exited
}

func (p *evaluatorProcess) failure() error {
	<-p.exited
	if p.cmd.ProcessState.ExitCode() == exitMemoryLimit {
		return fmt.Errorf("exceeded the memory limit of %d bytes", p.limit)
	}
	return fmt.Errorf("evaluator died (%s): %s", p.cmd.ProcessState, bytes.TrimSpace(p.stderr.bytes()))
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

type tailBuffer struct {
	limit int
	mu    sync.Mutex
	kept  []byte
}

func (t *tailBuffer) Write(written []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kept = append(t.kept, written...)
	if overflow := len(t.kept) - t.limit; overflow > 0 {
		t.kept = t.kept[overflow:]
	}
	return len(written), nil
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bytes.Clone(t.kept)
}
