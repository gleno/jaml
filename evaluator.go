package jaml

import (
	"context"
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
	os.Exit(serveEvaluations(os.NewFile(responsesFD, "jaml-responses")))
}

func serveEvaluations(answers io.Writer) int {
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
	if world.runtime.builtins == frozenBuiltins {
		chartTheIntrinsics()
	}
	var replies = &_ReplyWriter{out: answers, frames: framesFor(world.limit)}
	replies.greet(_Hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: world.nonce})
	var host = _Host{runtime: world.runtime, scripts: newScriptCache(world.scriptBudget)}
	var serving = &_Evaluator{host: host, buildTimeout: world.buildTimeout, watch: watch, replies: replies, jobs: newJobTable(stuckAfterCancel)}
	go watch.pulse(replies.beat)
	go serving.obey(&_FrameReader{source: os.NewFile(controlFD, "jaml-control"), limit: controlFrameCap, refusal: oversizedControl(), quiet: &_Quiet{}})
	return serving.follow(replies.frames.requests(os.NewFile(requestsFD, "jaml-requests")))
}

func stuckAfterCancel(job uint64) {
	fmt.Fprintf(os.Stderr, "jaml evaluator: job %d was still running %s after it was told to stop\n", job, cancelGrace)
	os.Exit(exitStuckAfterCancel)
}

type _Evaluator struct {
	host         _Host
	buildTimeout time.Duration
	watch        *_HeapWatch
	replies      *_ReplyWriter
	jobs         *_JobTable
}

func (e *_Evaluator) follow(requests *_FrameReader) int {
	for {
		incoming, err := e.receive(requests)
		if errors.Is(err, errHungUp) {
			return 0
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading request: %v\n", err)
			return exitProtocolBroken
		}
		if err := e.accept(incoming); err != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: %v\n", err)
			return exitProtocolBroken
		}
	}
}

func (e *_Evaluator) receive(requests *_FrameReader) (_Request, error) {
	var incoming _Request
	size, err := requests.size()
	if err != nil {
		return incoming, err
	}
	e.watch.during(func() { err = requests.into(size, &incoming) })
	return incoming, err
}

func (e *_Evaluator) obey(control *_FrameReader) {
	for {
		var incoming _Request
		size, err := control.size()
		if err == nil {
			err = control.into(size, &incoming)
		}
		if err != nil {
			if errors.Is(err, errHungUp) {
				return
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading control: %v\n", err)
			os.Exit(exitProtocolBroken)
		}
		switch incoming.Op {
		case opCancel:
			e.jobs.cancel(incoming.Job)
		case opForget:
			e.jobs.forget(incoming.Unit)
		default:
			fmt.Fprintf(os.Stderr, "jaml evaluator: a malformed %q control message\n", incoming.Op)
			os.Exit(exitProtocolBroken)
		}
	}
}

func (e *_Evaluator) accept(incoming _Request) error {
	switch {
	case incoming.Op == opBuild && incoming.Plan != nil:
		ctx, err := e.jobs.begin(incoming.Job)
		if err != nil {
			return err
		}
		go e.build(ctx, incoming.Job, *incoming.Plan)
	case incoming.Op == opServe && incoming.Work != nil:
		ctx, err := e.jobs.begin(incoming.Job)
		if err != nil {
			return err
		}
		var held = e.jobs.holding(incoming.Unit)
		if held == nil {
			e.jobs.settle(incoming.Job)
			e.replies.write(e.replies.frames.missing(incoming.Job))
			return nil
		}
		go e.serve(ctx, incoming.Job, held, *incoming.Work)
	default:
		return fmt.Errorf("a malformed %q request", incoming.Op)
	}
	return nil
}

func (e *_Evaluator) build(ctx context.Context, job uint64, plan _Blueprint) {
	var built _Servable
	var err error
	e.watch.during(func() { built, err = plan.build(ctx) })
	switch {
	case err != nil:
		e.jobs.settle(job)
		e.replies.write(e.replies.frames.failure(job, rootPlace, err))
	case e.jobs.keep(job, built):
		e.replies.write(e.builtFrame(job, built))
	default:
		e.replies.write(e.replies.frames.failure(job, rootPlace, cancelled(ctx)))
	}
}

func (e *_Evaluator) builtFrame(job uint64, built _Servable) []byte {
	var outline = built.outline()
	if outline != nil && outline.checked() != nil {
		outline = &_CodeOutline{}
	}
	frame, err := e.replies.frames.built(job, outline)
	if err != nil {
		frame, _ = e.replies.frames.built(job, &_CodeOutline{})
	}
	return frame
}

func (e *_Evaluator) serve(ctx context.Context, job uint64, held _Servable, request _Work) {
	var path = selectedPath(request.Path).String()
	prepared, err := e.prepare(job, held, request)
	if err != nil {
		e.jobs.settle(job)
		e.replies.write(e.replies.frames.failure(job, path, err))
		return
	}
	var sealed []byte
	e.watch.during(func() {
		bounded, stop := request.TimeLimit.bound(ctx)
		defer stop()
		var unwatch = context.AfterFunc(bounded, func() { e.overran(job, prepared.spot, bounded) })
		defer unwatch()
		result, err := prepared.render(bounded)
		var told = e.jobs.settle(job)
		switch {
		case err != nil:
			sealed = e.replies.frames.failure(job, path, err)
		case told:
			sealed = e.replies.frames.failure(job, path, cancelled(bounded))
		default:
			sealed = e.replies.frames.answer(job, result)
		}
	})
	e.replies.write(sealed)
}

func (e *_Evaluator) prepare(job uint64, held _Servable, request _Work) (_Prepared, error) {
	var overdue = fmt.Errorf("cancelled: %w", overdueBuild(e.buildTimeout))
	var watch = time.AfterFunc(e.buildTimeout, func() {
		e.jobs.overrun(job, func() { e.replies.write(e.replies.frames.failure(job, rootPlace, overdue)) })
	})
	prepared, err := held.prepare(request, e.host)
	if !watch.Stop() {
		return _Prepared{}, overdue
	}
	return prepared, err
}

func (e *_Evaluator) overran(job uint64, spot *_Spot, bounded context.Context) {
	var exhausted, overran = context.Cause(bounded).(_TimeExhausted)
	if !overran {
		return
	}
	e.jobs.overrun(job, func() {
		var stuck = spot.blame(fmt.Errorf("%w and was still inside a built-in %s after it was told to stop", exhausted, cancelGrace))
		e.replies.write(e.replies.frames.failure(job, stuck.path, stuck))
	})
}

type _ReplyWriter struct {
	mu     sync.Mutex
	out    io.Writer
	frames _Frames
}

func (w *_ReplyWriter) greet(greeting _Hello) {
	encoded, err := wireJSON(greeting)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing the greeting: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.out.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing the greeting: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
}

func (w *_ReplyWriter) beat() {
	w.write(nil)
}

func (w *_ReplyWriter) write(frame []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.out.Write(header(len(frame))); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", err)
		os.Exit(exitProtocolBroken)
	}
	if _, err := w.out.Write(frame); err != nil {
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
	builtins, known := builtinsNamed(os.Getenv(evaluatorBuiltins))
	if !known {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorBuiltins, os.Getenv(evaluatorBuiltins))
	}
	written, err = strconv.ParseInt(os.Getenv(evaluatorCallDepth), 10, 64)
	if err != nil {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorCallDepth, os.Getenv(evaluatorCallDepth))
	}
	callDepth, err := makeCallDepthLimit(int(written))
	if err != nil {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %w", evaluatorCallDepth, err)
	}
	scriptBudget, err := strconv.ParseInt(os.Getenv(evaluatorScripts), 10, 64)
	if err != nil || scriptBudget < 1 {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorScripts, os.Getenv(evaluatorScripts))
	}
	buildTimeout, err := strconv.ParseInt(os.Getenv(evaluatorBuildTime), 10, 64)
	if err != nil || buildTimeout < 1 {
		return _EvaluatorWorld{}, fmt.Errorf("bad %s: %q", evaluatorBuildTime, os.Getenv(evaluatorBuildTime))
	}
	return _EvaluatorWorld{limit: limit, runtime: _Runtime{builtins: builtins, callDepth: callDepth}, scriptBudget: scriptBudget, buildTimeout: time.Duration(buildTimeout), nonce: nonce, parent: parent}, nil
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

func (w *_HeapWatch) pulse(beat func()) {
	for range time.Tick(heartbeatEvery) {
		if w.working() {
			beat()
		}
	}
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
