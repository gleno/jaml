package jaml

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type buildRace struct {
	bound   context.Context
	stop    context.CancelFunc
	once    sync.Once
	settled chan struct{}
	verdict outcome
}

func raceBuild(parent context.Context, timeout time.Duration) *buildRace {
	bound, stop := context.WithTimeoutCause(parent, timeout, fmt.Errorf("the build took longer than its timeout of %s: %w", timeout, context.DeadlineExceeded))
	race := &buildRace{bound: bound, stop: stop, settled: make(chan struct{})}
	context.AfterFunc(bound, func() { race.record(refused{err: cancelled(bound)}) })
	return race
}

func (r *buildRace) finish(result outcome) bool {
	won := r.record(result)
	r.stop()
	return won
}

func (r *buildRace) record(result outcome) bool {
	won := false
	r.once.Do(func() {
		r.verdict, won = result, true
		close(r.settled)
	})
	return won
}

func (r *buildRace) done() <-chan struct{} {
	return r.settled
}

func (r *buildRace) await() outcome {
	<-r.settled
	return r.verdict
}
