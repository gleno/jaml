package jaml

import (
	"context"
	"time"
)

type _TrustedEngine struct {
	gate         chan struct{}
	buildTimeout time.Duration
	host         _Host
}

type _LocalUnit struct {
	engine *_TrustedEngine
	built  _Servable
}

func newTrusted(configured _SandboxSettings) *_TrustedEngine {
	return &_TrustedEngine{gate: make(chan struct{}, configured.maxConcurrency), buildTimeout: configured.buildTimeout, host: _Host{runtime: configured.runtime, scripts: newScriptCache(configured.trustedScriptBudget())}}
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
	var built _Servable
	var err = e.raced(ctx, func(bound context.Context) (err error) {
		built, err = plan.build(bound)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &_LocalUnit{engine: e, built: built}, nil
}

func (e *_TrustedEngine) raced(ctx context.Context, work func(bound context.Context) error) error {
	if err := e.enter(ctx); err != nil {
		return err
	}
	var race = raceBuild(ctx, e.buildTimeout)
	go func() {
		defer e.leave()
		if err := work(race.bound); err != nil {
			race.finish(_Refused{err: err})
			return
		}
		race.finish(_Built{})
	}()
	if failed, isRefused := race.await().(_Refused); isRefused {
		return failed.err
	}
	return nil
}

func (u *_LocalUnit) serve(ctx context.Context, ask _Ask) _Outcome {
	written, err := writeWork(ask, unboundedAsk)
	if err != nil {
		return _Refused{err: err}
	}
	var prepared _Prepared
	err = u.engine.raced(ctx, func(context.Context) (err error) {
		prepared, err = u.built.prepare(written.view(), u.engine.host)
		return err
	})
	if err != nil {
		return _Refused{err: err}
	}
	if err := u.engine.enter(ctx); err != nil {
		return _Refused{err: err}
	}
	defer u.engine.leave()
	bounded, stop := written.ask.timeLimit.bound(ctx)
	defer stop()
	result, err := prepared.render(bounded)
	if err != nil {
		return _Refused{err: err}
	}
	data, err := encodeResult(result)
	if err != nil {
		return _Refused{err: err}
	}
	return _Answered{data: data, skipped: result.skipped}
}
