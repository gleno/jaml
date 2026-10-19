package jaml

import (
	"context"
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

func newTrusted(configured _SandboxSettings) *trustedEngine {
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
	race := raceBuild(ctx, e.buildTimeout)
	var built servable
	go func() {
		defer e.leave()
		made, err := plan.build(race.bound)
		if err != nil {
			race.finish(refused{err: err})
			return
		}
		built = made
		race.finish(answered{})
	}()
	if failed, isRefused := race.await().(refused); isRefused {
		return nil, failed.err
	}
	return &localUnit{engine: e, built: built}, nil
}

func (u *localUnit) serve(ctx context.Context, request work) outcome {
	if err := u.engine.enter(ctx); err != nil {
		return refused{err: err}
	}
	defer u.engine.leave()
	run, err := u.built.prepare(request)
	if err != nil {
		return refused{err: err}
	}
	result, err := run(ctx)
	if err != nil {
		return refused{err: err}
	}
	data, err := encodeResult(result)
	if err != nil {
		return refused{err: err}
	}
	return answered{data: data, skipped: result.skipped}
}
