package jaml

import (
	"context"
	"time"
)

type _TrustedEngine struct {
	gate         chan struct{}
	buildTimeout time.Duration
	buildLimit   _TimeLimit
	host         _Host
}

type _LocalUnit struct {
	engine *_TrustedEngine
	built  _Servable
}

func newTrusted(configured _SandboxSettings) *_TrustedEngine {
	return &_TrustedEngine{gate: make(chan struct{}, configured.maxConcurrency), buildTimeout: configured.buildTimeout, buildLimit: configured.buildTimeLimit, host: _Host{runtime: configured.runtime, scripts: newScriptCache(configured.trustedScriptBudget())}}
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

func (e *_TrustedEngine) health() Health {
	return Health{Resident: residentBytes()}
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
	go onALockedThread(func() {
		defer e.leave()
		budgeted, release := e.buildLimit.enforce(race.bound, startMeter(), true)
		defer release()
		var unwatch = context.AfterFunc(budgeted, func() { race.record(_Refused{err: cancelled(budgeted)}) })
		defer unwatch()
		if err := work(budgeted); err != nil {
			race.finish(_Refused{err: err})
			return
		}
		race.finish(_Built{})
	})
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
	var served _Outcome
	onALockedThread(func() {
		var meter = startMeter()
		bounded, stop := written.ask.wallLimit.bound(ctx)
		defer stop()
		budgeted, release := written.ask.timeLimit.enforce(bounded, meter, false)
		defer release()
		result, err := prepared.render(budgeted, ask.console.speaker())
		var used = meter.used()
		if err != nil {
			served = _Refused{err: err, used: &used}
			return
		}
		data, err := encodeResult(result)
		if err != nil {
			served = _Refused{err: err, used: &used}
			return
		}
		served = _Answered{data: data, skipped: result.skipped, used: used}
	})
	return served
}
