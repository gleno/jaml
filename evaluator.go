package jaml

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	goruntime "runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
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
	guard := guardTheEvaluator(world)
	if guard.orphaned() {
		return exitOrphaned
	}
	requests := json.NewDecoder(os.NewFile(requestsFD, "jaml-requests"))
	responses := json.NewEncoder(os.NewFile(responsesFD, "jaml-responses"))
	if err := responses.Encode(hello{Magic: evaluatorMagic, Version: protocolVersion, Nonce: world.nonce}); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: greeting: %v\n", err)
		return exitProtocolBroken
	}
	var server evaluationServer
	for {
		var next request
		if err := requests.Decode(&next); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "jaml evaluator: reading request: %v\n", err)
			return exitProtocolBroken
		}
		var written error
		guard.during(func() { written = responses.Encode(server.handle(next)) })
		if written != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", written)
			return exitProtocolBroken
		}
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

type evaluationServer struct {
	compiled []expression
	session  *session
}

func (s *evaluationServer) handle(next request) response {
	switch next.Op {
	case opCompile:
		return s.compile(next.Sources)
	case opOpen:
		opened, err := openSession(next.Vars)
		if err != nil {
			return response{Error: err.Error()}
		}
		s.session = opened
		return response{}
	case opEval:
		return s.evaluate(next.ID)
	default:
		return response{Error: fmt.Sprintf("unknown operation %q", next.Op)}
	}
}

func (s *evaluationServer) compile(sources []source) response {
	s.compiled = make([]expression, len(sources))
	for id, written := range sources {
		compiled, err := compileExpression(written.Text, written.Interpolated)
		if err != nil {
			return response{Error: fmt.Sprintf("expression %d: %s", id, err)}
		}
		compiled.id = id
		s.compiled[id] = compiled
	}
	return response{}
}

func (s *evaluationServer) evaluate(id int) response {
	if s.session == nil {
		return response{Error: "no open session"}
	}
	if id < 0 || id >= len(s.compiled) {
		return response{Error: fmt.Sprintf("no expression %d", id)}
	}
	result, err := s.session.evaluate(s.compiled[id])
	if err != nil {
		return response{Error: err.Error()}
	}
	if _, isUndefined := result.(undefined); isUndefined {
		return response{Undefined: true}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return response{Error: err.Error()}
	}
	return response{Value: encoded}
}
