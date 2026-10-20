package jaml

import (
	"context"
	"time"
)

type _TrustedEngine struct {
	gate         chan struct{}
	buildTimeout time.Duration
}

type _LocalUnit struct {
	engine *_TrustedEngine
	built  _Servable
}

func newTrusted(configured _SandboxSettings) *_TrustedEngine {
	return &_TrustedEngine{gate: make(chan struct{}, configured.maxConcurrency), buildTimeout: configured.buildTimeout}
}

func (e *_TrustedEngine) enter(ctx context.Context) error {
	select {
	case e.gate <- struct{}{}:
		return nil
	default:
	}
	select {
	case e.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return cancelled(ctx)
	}
}

func (e *_TrustedEngine) leave() {
	<-e.gate
}

func (e *_TrustedEngine) build(ctx context.Context, plan _Blueprint) (_Unit, error) {
	if err := e.enter(ctx); err != nil {
		return nil, err
	}
	var race = raceBuild(ctx, e.buildTimeout)
	var built _Servable
	go func() {
		defer e.leave()
		made, err := plan.build(race.bound)
		if err != nil {
			race.finish(_Refused{err: err})
			return
		}
		built = made
		race.finish(_Built{})
	}()
	if failed, isRefused := race.await().(_Refused); isRefused {
		return nil, failed.err
	}
	return &_LocalUnit{engine: e, built: built}, nil
}

func (u *_LocalUnit) serve(ctx context.Context, ask _Ask) _Outcome {
	written, err := writeWork(ask, unboundedAsk)
	if err != nil {
		return _Refused{err: err}
	}
	if err := u.engine.enter(ctx); err != nil {
		return _Refused{err: err}
	}
	defer u.engine.leave()
	run, err := u.built.prepare(written.view(), mutableBuiltins)
	if err != nil {
		return _Refused{err: err}
	}
	bounded, stop := written.ask.timeLimit.bound(ctx)
	defer stop()
	result, err := run(bounded)
	if err != nil {
		return _Refused{err: err}
	}
	data, err := encodeResult(result)
	if err != nil {
		return _Refused{err: err}
	}
	return _Answered{data: data, skipped: result.skipped}
}
