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
	gcCyclesMetric     = "/gc/cycles/total:gc-cycles"
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
	var watch = watchTheHeap(world)
	if watch.orphaned() {
		return exitOrphaned
	}
	var replies = &_ReplyWriter{out: os.NewFile(responsesFD, "jaml-responses"), frames: framesFor(world.limit)}
	replies.greet(_Hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: world.nonce})
	var serving = &_Evaluator{watch: watch, replies: replies, units: map[uint64]_Servable{}, running: map[uint64]context.CancelFunc{}, ended: map[uint64]struct{}{}}
	go serving.obey(json.NewDecoder(os.NewFile(controlFD, "jaml-control")))
	return serving.follow(json.NewDecoder(os.NewFile(requestsFD, "jaml-requests")))
}

type _Evaluator struct {
	watch   *_HeapWatch
	replies *_ReplyWriter
	mu      sync.Mutex
	arrived uint64
	units   map[uint64]_Servable
	running map[uint64]context.CancelFunc
	ended   map[uint64]struct{}
}

func (e *_Evaluator) follow(requests *json.Decoder) int {
	for {
		var incoming _Request
		if err := requests.Decode(&incoming); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading request: %v\n", err)
			return exitProtocolBroken
		}
		if err := e.accept(incoming); err != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: %v\n", err)
			return exitProtocolBroken
		}
	}
}

func (e *_Evaluator) obey(control *json.Decoder) {
	for {
		var incoming _Request
		if err := control.Decode(&incoming); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading control: %v\n", err)
			os.Exit(exitProtocolBroken)
		}
		switch incoming.Op {
		case opCancel:
			e.end(incoming.Job)
		case opForget:
			e.forget(incoming.Unit)
		default:
			fmt.Fprintf(os.Stderr, "jaml evaluator: a malformed %q control message\n", incoming.Op)
			os.Exit(exitProtocolBroken)
		}
	}
}

func (e *_Evaluator) accept(incoming _Request) error {
	switch {
	case incoming.Op == opBuild && incoming.Plan != nil:
		ctx, err := e.begin(incoming.Job)
		if err != nil {
			return err
		}
		go e.build(ctx, incoming.Job, *incoming.Plan)
	case incoming.Op == opServe && incoming.Work != nil:
		ctx, err := e.begin(incoming.Job)
		if err != nil {
			return err
		}
		var held = e.holding(incoming.Unit)
		if held == nil {
			e.finish(incoming.Job)
			e.replies.write(e.replies.frames.missing(incoming.Job))
			return nil
		}
		go e.serve(ctx, incoming.Job, held, *incoming.Work)
	default:
		return fmt.Errorf("a malformed %q request", incoming.Op)
	}
	return nil
}

func (e *_Evaluator) begin(job uint64) (context.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if job <= e.arrived {
		return nil, fmt.Errorf("job %d arrived after job %d", job, e.arrived)
	}
	e.arrived = job
	ctx, cancel := context.WithCancel(context.Background())
	e.running[job] = cancel
	if _, gone := e.ended[job]; gone {
		delete(e.ended, job)
		cancel()
	}
	return ctx, nil
}

func (e *_Evaluator) holding(unit uint64) _Servable {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.units[unit]
}

func (e *_Evaluator) end(job uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel := e.running[job]; cancel != nil {
		cancel()
		return
	}
	if job > e.arrived {
		e.ended[job] = struct{}{}
	}
}

func (e *_Evaluator) forget(unit uint64) {
	e.mu.Lock()
	delete(e.units, unit)
	e.mu.Unlock()
	e.end(unit)
}

func (e *_Evaluator) build(ctx context.Context, job uint64, plan _Blueprint) {
	var built _Servable
	var err error
	e.watch.during(func() { built, err = plan.build(ctx) })
	e.mu.Lock()
	if err == nil && ctx.Err() == nil {
		e.units[job] = built
	}
	e.mu.Unlock()
	e.finish(job)
	if err != nil {
		e.replies.write(e.replies.frames.failure(job, "", err))
		return
	}
	e.replies.write(e.replies.frames.built(job))
}

func (e *_Evaluator) serve(ctx context.Context, job uint64, held _Servable, request _Work) {
	defer e.finish(job)
	run, err := held.prepare(request)
	if err != nil {
		e.replies.write(e.replies.frames.failure(job, "", err))
		return
	}
	var sealed []byte
	e.watch.during(func() {
		result, err := run(ctx)
		if err != nil {
			sealed = e.replies.frames.failure(job, selectedPath(request.Path).String(), err)
			return
		}
		sealed = e.replies.frames.answer(job, result)
	})
	e.replies.write(sealed)
}

func (e *_Evaluator) finish(job uint64) {
	e.mu.Lock()
	var cancel = e.running[job]
	delete(e.running, job)
	e.mu.Unlock()
	cancel()
}

type _ReplyWriter struct {
	mu     sync.Mutex
	out    *os.File
	frames _Frames
}

func (w *_ReplyWriter) greet(greeting _Hello) {
	encoded, err := wireJSON(greeting)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing the greeting: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
	w.write(encoded)
}

func (w *_ReplyWriter) write(frame []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.out.Write(frame); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
	if _, err := w.out.Write([]byte{'\n'}); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
}

func adoptTheHostsZone() {
	var name = os.Getenv("TZ")
	if name == "" || filepath.IsAbs(name) {
		return
	}
	if zone, err := time.LoadLocation(name); err == nil {
		time.Local = zone
	}
}

func worldFromEnv() (_EvaluatorWorld, error) {
	written, err := strconv.ParseInt(os.Getenv(evaluatorMemoryLimit), 10, 64)
	if err != nil {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorMemoryLimit, os.Getenv(evaluatorMemoryLimit))
	}
	limit, err := makeMemoryLimit(written)
	if err != nil {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %w", evaluatorMemoryLimit, err)
	}
	parent, err := strconv.Atoi(os.Getenv(evaluatorParent))
	if err != nil || parent <= 0 {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorParent, os.Getenv(evaluatorParent))
	}
	var nonce = os.Getenv(evaluatorNonce)
	if nonce == "" {
		return _EvaluatorWorld{}, fmt.Errorf("no %s", evaluatorNonce)
	}
	return _EvaluatorWorld{limit: limit, nonce: nonce, parent: parent}, nil
}

type _HeapWatch struct {
	ceiling    uint64
	parent     int
	wake       chan struct{}
	quietUntil uint64
	mu         sync.Mutex
	busy       int
}

func watchTheHeap(world _EvaluatorWorld) *_HeapWatch {
	goruntime.GC()
	var ceiling = min(heapHeld()+uint64(world.limit), math.MaxInt64)
	debug.SetMemoryLimit(int64(ceiling))
	var watch = &_HeapWatch{ceiling: ceiling, parent: world.parent, wake: make(chan struct{}, 1)}
	go watch.patrol()
	return watch
}

func heapHeld() uint64 {
	return metric(heapObjectsMetric)
}

func gcCycles() uint64 {
	return metric(gcCyclesMetric)
}

func metric(name string) uint64 {
	var samples = []metrics.Sample{{Name: name}}
	metrics.Read(samples)
	return samples[0].Value.Uint64()
}

func (w *_HeapWatch) during(work func()) {
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

func (w *_HeapWatch) patrol() {
	for range w.wake {
		for w.inspect(); w.working(); w.inspect() {
			time.Sleep(memoryCheckEvery)
		}
	}
}

func liveHeap() uint64 {
	goruntime.GC()
	return heapHeld()
}

func (w *_HeapWatch) confirmOverflow() {
	if held := liveHeap(); held > w.ceiling {
		fmt.Fprintf(os.Stderr, "jaml evaluator: live heap reached %d bytes, over its starting heap plus the limit, %d\n", held, w.ceiling)
		os.Exit(exitMemoryLimit)
	}
}

func (w *_HeapWatch) working() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.busy > 0
}

func (w *_HeapWatch) inspect() {
	var held = heapHeld()
	w.notePressure(held)
	if held > w.ceiling {
		w.confirmOverflow()
	}
	if w.orphaned() {
		os.Exit(exitOrphaned)
	}
}

func (w *_HeapWatch) notePressure(held uint64) {
	var line = w.ceiling / 4 * 3
	if held <= line || gcCycles() <= w.quietUntil {
		return
	}
	if liveHeap() > line {
		fmt.Fprintln(os.Stderr, pressureMarker)
		w.quietUntil = math.MaxUint64
		return
	}
	w.quietUntil = gcCycles()
}

func (w *_HeapWatch) orphaned() bool {
	return os.Getppid() != w.parent
}
