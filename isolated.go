package jaml

import (
	"context"
	"errors"
)

const servingAttempts = 2

type _IsolatedEngine struct {
	dispatcher *_Dispatcher
	frames     _Frames
}

type _RemoteUnit struct {
	dispatcher *_Dispatcher
	frames     _Frames
	plan       _Blueprint
	lifetime   context.Context
	outline    *_CodeOutline
}

func startIsolated(lifetime context.Context, configured _SandboxSettings) (*_IsolatedEngine, error) {
	scriptBudget, err := configured.childScriptBudget()
	if err != nil {
		return nil, err
	}
	var spec = _ChildSpec{lifetime: lifetime, limit: configured.memoryLimitOrDefault(), runtime: configured.runtime, scriptBudget: scriptBudget, startDeadline: evaluatorStartDeadline, buildTimeout: configured.buildTimeout, buildLimit: configured.buildTimeLimit, jobs: configured.maxConcurrency}
	first, err := startLife(spec)
	if err != nil {
		return nil, err
	}
	var start = func() (*_Life, error) { return startLife(spec) }
	return &_IsolatedEngine{dispatcher: newDispatcher(_DispatcherSpec{limit: configured.maxConcurrency, start: start, heard: configured.attributedDeaths}, first), frames: framesFor(spec.limit)}, nil
}

func (e *_IsolatedEngine) health() Health {
	return e.dispatcher.health()
}

func (e *_IsolatedEngine) build(lifetime context.Context, plan _Blueprint) (_Unit, error) {
	var made = &_RemoteUnit{dispatcher: e.dispatcher, frames: e.frames, plan: plan, lifetime: lifetime}
	switch settled := e.dispatcher.carry(lifetime, made.reside).(type) {
	case _Refused:
		return nil, settled.err
	case _Built:
		made.outline = settled.outline
	}
	return made, nil
}

func (u *_RemoteUnit) reside(e *_Errand, l *_Life) _Outcome {
	return l.reside(u, e.report).result
}

func (u *_RemoteUnit) holdsCode(path []string) bool {
	return u.outline.holds(path)
}

func (u *_RemoteUnit) request() (*_RequestBody, error) {
	return writePlan(u.plan, u.frames.limit)
}

func (u *_RemoteUnit) serve(ctx context.Context, ask _Ask) _Outcome {
	ask.codeFree = len(ask.scripts) == 0 && !u.holdsCode(ask.path)
	written, err := writeWork(ask, u.frames.limit)
	if err != nil {
		return _Refused{err: err}
	}
	var outgoing = _Outgoing{body: written.body, console: ask.console}
	return u.dispatcher.carry(ctx, func(e *_Errand, l *_Life) _Outcome {
		for range servingAttempts {
			var r = l.reside(u, e.report)
			if _, built := r.result.(_Built); !built {
				return r.result
			}
			var asked = outgoing
			asked.unit = r.build
			var served = l.serve(e.ctx, asked)
			if failed, isRefused := served.(_Refused); !isRefused || !errors.Is(failed.err, errMissing) {
				return served
			}
			l.drop(r)
		}
		return _Refused{err: errMissing}
	})
}
