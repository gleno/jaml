package jaml

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type _BuildRace struct {
	bound   context.Context
	stop    context.CancelFunc
	once    sync.Once
	settled chan struct{}
	verdict _Outcome
}

func raceBuild(parent context.Context, timeout time.Duration) *_BuildRace {
	bound, stop := context.WithTimeoutCause(parent, timeout, fmt.Errorf("the build took longer than its timeout of %s: %w", timeout, context.DeadlineExceeded))
	var race = &_BuildRace{bound: bound, stop: stop, settled: make(chan struct{})}
	context.AfterFunc(bound, func() { race.record(_Refused{err: cancelled(bound)}) })
	return race
}

func (r *_BuildRace) finish(result _Outcome) bool {
	var won = r.record(result)
	r.stop()
	return won
}

func (r *_BuildRace) record(result _Outcome) bool {
	var won = false
	r.once.Do(func() {
		r.verdict, won = result, true
		close(r.settled)
	})
	return won
}

func (r *_BuildRace) done() <-chan struct{} {
	return r.settled
}

func (r *_BuildRace) await() _Outcome {
	<-r.settled
	return r.verdict
}
