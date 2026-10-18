package jaml

import (
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
	"time"
)

var errTemplateRefused = errors.New("the template was refused")

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
	guard := guardTheEvaluator(world)
	if guard.orphaned() {
		return exitOrphaned
	}
	requestsFile := os.NewFile(requestsFD, "jaml-requests")
	requests := json.NewDecoder(requestsFile)
	responses := json.NewEncoder(os.NewFile(responsesFD, "jaml-responses"))
	if err := responses.Encode(hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: world.nonce}); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: greeting: %v\n", err)
		return exitProtocolBroken
	}
	built, err := receiveTemplate(requests, responses)
	if errors.Is(err, errTemplateRefused) {
		_, _ = io.Copy(io.Discard, requestsFile)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: %v\n", err)
		return exitProtocolBroken
	}
	for {
		var job rendering
		if err := requests.Decode(&job); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading request: %v\n", err)
			return exitProtocolBroken
		}
		var written error
		guard.during(func() { written = responses.Encode(answer(built.render(job))) })
		if written != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", written)
			return exitProtocolBroken
		}
	}
}

func receiveTemplate(requests *json.Decoder, responses *json.Encoder) (*template, error) {
	var plan blueprint
	if err := requests.Decode(&plan); err != nil {
		return nil, fmt.Errorf("reading the template: %w", err)
	}
	built, err := plan.build()
	if err != nil {
		refused := describeFailure(err)
		return nil, errors.Join(errTemplateRefused, responses.Encode(&refused))
	}
	return built, responses.Encode((*failure)(nil))
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

type evaluatorGuard struct {
	ceiling uint64
	parent  int
}

func guardTheEvaluator(world evaluatorWorld) evaluatorGuard {
	goruntime.GC()
	ceiling := min(heapHeld()+uint64(world.limit), math.MaxInt64)
	debug.SetMemoryLimit(int64(ceiling))
	return evaluatorGuard{ceiling: ceiling, parent: world.parent}
}

func heapHeld() uint64 {
	samples := []metrics.Sample{{Name: heapObjectsMetric}}
	metrics.Read(samples)
	return samples[0].Value.Uint64()
}

func (g evaluatorGuard) during(work func()) {
	done := make(chan struct{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		g.watch(done)
	}()
	work()
	close(done)
	<-watching
}

func (g evaluatorGuard) watch(done <-chan struct{}) {
	ticker := time.NewTicker(memoryCheckEvery)
	defer ticker.Stop()
	for {
		if held := heapHeld(); held > g.ceiling {
			fmt.Fprintf(os.Stderr, "jaml evaluator: heap reached %d bytes, over its starting heap plus the limit, %d\n", held, g.ceiling)
			os.Exit(exitMemoryLimit)
		}
		if g.orphaned() {
			os.Exit(exitOrphaned)
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func (g evaluatorGuard) orphaned() bool {
	return os.Getppid() != g.parent
}
