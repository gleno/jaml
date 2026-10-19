package jaml

import (
	"context"
	"fmt"
	"time"
)

type trustedEngine struct {
	gate         chan struct{}
	buildTimeout time.Duration
}

type localUnit struct {
	engine *trustedEngine
	built  servable
}

type finishedBuild struct {
	built servable
	err   error
}

func newTrusted(configured sandboxSettings) *trustedEngine {
	return &trustedEngine{gate: make(chan struct{}, configured.maxConcurrency), buildTimeout: configured.buildTimeout}
}

func (e *trustedEngine) enter(ctx context.Context) error {
	select {
	case e.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return cancelled(ctx)
	}
}

func (e *trustedEngine) leave() {
	<-e.gate
}

func (e *trustedEngine) build(ctx context.Context, plan blueprint) (unit, error) {
	if err := e.enter(ctx); err != nil {
		return nil, err
	}
	bounded, stop := context.WithTimeoutCause(ctx, e.buildTimeout, fmt.Errorf("the build took longer than its timeout of %s: %w", e.buildTimeout, context.DeadlineExceeded))
	defer stop()
	finished := make(chan finishedBuild, 1)
	go func() {
		defer e.leave()
		built, err := plan.build(bounded)
		finished <- finishedBuild{built: built, err: err}
	}()
	select {
	case done := <-finished:
		if done.err != nil {
			return nil, done.err
		}
		return &localUnit{engine: e, built: done.built}, nil
	case <-bounded.Done():
		return nil, cancelled(bounded)
	}
}

func (u *localUnit) serve(ctx context.Context, request work) outcome {
	if err := u.engine.enter(ctx); err != nil {
		return refused{err: err}
	}
	defer u.engine.leave()
	ready, err := u.built.prepare(request)
	if err != nil {
		return refused{err: err}
	}
	data, err := ready.run(ctx)
	if err != nil {
		return refused{at: ready.at, err: err}
	}
	return answered{at: ready.at, data: data}
}
