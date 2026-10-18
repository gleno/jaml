package jaml

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"time"
)

const (
	heapObjectsMetric  = "/memory/classes/heap/objects:bytes"
	memoryCheckEvery   = time.Millisecond
	exitProtocolBroken = 2
	exitOrphaned       = 3
)

func init() {
	if os.Getenv(evaluatorMarker) != "1" {
		return
	}
	os.Exit(serveEvaluations())
}

func serveEvaluations() int {
	limit, err := strconv.ParseInt(os.Getenv(evaluatorMemoryLimit), 10, 64)
	if err != nil || limit <= 0 {
		fmt.Fprintf(os.Stderr, "jaml evaluator: bad %s: %q\n", evaluatorMemoryLimit, os.Getenv(evaluatorMemoryLimit))
		return exitProtocolBroken
	}
	guardTheEvaluator(limit)
	requests := json.NewDecoder(os.NewFile(requestsFD, "jaml-requests"))
	responses := json.NewEncoder(os.NewFile(responsesFD, "jaml-responses"))
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
		if err := responses.Encode(server.handle(next)); err != nil {
			fmt.Fprintf(os.Stderr, "jaml evaluator: writing response: %v\n", err)
			return exitProtocolBroken
		}
	}
}

func guardTheEvaluator(limit int64) {
	debug.SetMemoryLimit(limit)
	limitAddressSpace(limit)
	parent := os.Getppid()
	go func() {
		samples := []metrics.Sample{{Name: heapObjectsMetric}}
		for {
			metrics.Read(samples)
			if held := samples[0].Value.Uint64(); held > uint64(limit) {
				fmt.Fprintf(os.Stderr, "jaml evaluator: heap reached %d bytes, over the limit of %d\n", held, limit)
				os.Exit(exitMemoryLimit)
			}
			if os.Getppid() != parent {
				os.Exit(exitOrphaned)
			}
			time.Sleep(memoryCheckEvery)
		}
	}()
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
	case opClose:
		s.session = nil
		return response{}
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
