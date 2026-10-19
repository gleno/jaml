package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"sync"
	"time"
)

const (
	heapObjectsMetric  = "/memory/classes/heap/objects:bytes"
	memoryCheckEvery   = time.Millisecond
	exitProtocolBroken = 87
	exitOrphaned       = 88
)

func init() {
	if os.Getenv(evaluatorMarker) != "1" {
		return
	}
	os.Exit(serveEvaluations())
}

func serveEvaluations() int {
	world, err := worldFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: %v\n", err)
		return exitProtocolBroken
	}
	adoptTheHostsZone()
	watch := watchTheHeap(world)
	if watch.orphaned() {
		return exitOrphaned
	}
	requests := json.NewDecoder(os.NewFile(requestsFD, "jaml-requests"))
	replies := &replyWriter{encoder: json.NewEncoder(os.NewFile(responsesFD, "jaml-responses"))}
	replies.send(hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: world.nonce})
	serving := &evaluator{watch: watch, replies: replies, units: map[uint64]servable{}, jobs: map[uint64]context.CancelFunc{}}
	for {
		var incoming request
		if err := requests.Decode(&incoming); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading request: %v\n", err)
			return exitProtocolBroken
		}
		if err := serving.accept(incoming); err != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: %v\n", err)
			return exitProtocolBroken
		}
	}
}

type evaluator struct {
	watch   *heapWatch
	replies *replyWriter
	mu      sync.Mutex
	units   map[uint64]servable
	jobs    map[uint64]context.CancelFunc
}

func (e *evaluator) accept(incoming request) error {
	switch {
	case incoming.Op == opBuild && incoming.Plan != nil:
		go e.build(incoming.Job, incoming.Unit, *incoming.Plan)
	case incoming.Op == opServe && incoming.Work != nil:
		ctx, cancel := context.WithCancel(context.Background())
		e.mu.Lock()
		e.jobs[incoming.Job] = cancel
		e.mu.Unlock()
		go e.serve(ctx, incoming.Job, incoming.Unit, *incoming.Work)
	case incoming.Op == opCancel:
		e.mu.Lock()
		cancel := e.jobs[incoming.Job]
		e.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case incoming.Op == opForget:
		e.mu.Lock()
		delete(e.units, incoming.Unit)
		e.mu.Unlock()
	default:
		return fmt.Errorf("a malformed %q request", incoming.Op)
	}
	return nil
}

func (e *evaluator) build(job, unit uint64, plan blueprint) {
	var built servable
	var err error
	e.watch.during(func() { built, err = plan.build() })
	if err != nil {
		refused := describeFailure(err)
		e.replies.send(reply{Job: job, Failure: &refused})
		return
	}
	e.mu.Lock()
	e.units[unit] = built
	e.mu.Unlock()
	e.replies.send(reply{Job: job, Built: true})
}

func (e *evaluator) serve(ctx context.Context, job, unit uint64, request work) {
	defer e.finish(job)
	e.mu.Lock()
	held := e.units[unit]
	e.mu.Unlock()
	if held == nil {
		missing := describeFailure(fmt.Errorf("the evaluator holds no unit %d", unit))
		e.replies.send(reply{Job: job, Failure: &missing})
		return
	}
	ready, err := held.prepare(request)
	if err != nil {
		refused := describeFailure(err)
		e.replies.send(reply{Job: job, Failure: &refused})
		return
	}
	e.replies.send(reply{Job: job, At: &ready.at})
	var data []byte
	e.watch.during(func() { data, err = ready.run(ctx) })
	if err != nil {
		failed := describeFailure(err)
		e.replies.send(reply{Job: job, Failure: &failed})
		return
	}
	e.replies.send(reply{Job: job, Data: data})
}

func (e *evaluator) finish(job uint64) {
	e.mu.Lock()
	cancel := e.jobs[job]
	delete(e.jobs, job)
	e.mu.Unlock()
	cancel()
}

type replyWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *replyWriter) send(outgoing any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.encoder.Encode(outgoing); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
}

func adoptTheHostsZone() {
	name := os.Getenv("TZ")
	if name == "" || filepath.IsAbs(name) {
		return
	}
	if zone, err := time.LoadLocation(name); err == nil {
		time.Local = zone
	}
}

func worldFromEnv() (evaluatorWorld, error) {
	written, err := strconv.ParseInt(os.Getenv(evaluatorMemoryLimit), 10, 64)
	if err != nil {
		return evaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorMemoryLimit, os.Getenv(evaluatorMemoryLimit))
	}
	limit, err := makeMemoryLimit(written)
	if err != nil {
		return evaluatorWorld{}, fmt.Errorf("bad %s: %w", evaluatorMemoryLimit, err)
	}
	parent, err := strconv.Atoi(os.Getenv(evaluatorParent))
	if err != nil || parent <= 0 {
		return evaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorParent, os.Getenv(evaluatorParent))
	}
	nonce := os.Getenv(evaluatorNonce)
	if nonce == "" {
		return evaluatorWorld{}, fmt.Errorf("no %s", evaluatorNonce)
	}
	return evaluatorWorld{limit: limit, nonce: nonce, parent: parent}, nil
}

type heapWatch struct {
	ceiling uint64
	parent  int
	wake    chan struct{}
	mu      sync.Mutex
	busy    int
}

func watchTheHeap(world evaluatorWorld) *heapWatch {
	goruntime.GC()
	ceiling := min(heapHeld()+uint64(world.limit), math.MaxInt64)
	debug.SetMemoryLimit(int64(ceiling))
	watch := &heapWatch{ceiling: ceiling, parent: world.parent, wake: make(chan struct{}, 1)}
	go watch.patrol()
	return watch
}

func heapHeld() uint64 {
	samples := []metrics.Sample{{Name: heapObjectsMetric}}
	metrics.Read(samples)
	return samples[0].Value.Uint64()
}

func (w *heapWatch) during(work func()) {
	w.mu.Lock()
	w.busy++
	if w.busy == 1 {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.busy--
		w.mu.Unlock()
	}()
	work()
}

func (w *heapWatch) patrol() {
	for range w.wake {
		for w.inspect(); w.working(); w.inspect() {
			time.Sleep(memoryCheckEvery)
		}
	}
}

func (w *heapWatch) confirmOverflow() {
	goruntime.GC()
	if held := heapHeld(); held > w.ceiling {
		fmt.Fprintf(os.Stderr, "jaml evaluator: live heap reached %d bytes, over its starting heap plus the limit, %d\n", held, w.ceiling)
		os.Exit(exitMemoryLimit)
	}
}

func (w *heapWatch) working() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy > 0
}

func (w *heapWatch) inspect() {
	if heapHeld() > w.ceiling {
		w.confirmOverflow()
	}
	if w.orphaned() {
		os.Exit(exitOrphaned)
	}
}

func (w *heapWatch) orphaned() bool {
	return os.Getppid() != w.parent
}
