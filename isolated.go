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
}

func startIsolated(lifetime context.Context, configured _SandboxSettings) (*_IsolatedEngine, error) {
	var spec = _ChildSpec{lifetime: lifetime, limit: configured.memoryLimitOrDefault(), startDeadline: evaluatorStartDeadline, buildTimeout: configured.buildTimeout}
	first, err := startLife(spec)
	if err != nil {
		return nil, err
	}
	var start = func() (*_Life, error) { return startLife(spec) }
	return &_IsolatedEngine{dispatcher: newDispatcher(configured.maxConcurrency, start, first), frames: framesFor(spec.limit)}, nil
}

func (e *_IsolatedEngine) build(lifetime context.Context, plan _Blueprint) (_Unit, error) {
	var made = &_RemoteUnit{dispatcher: e.dispatcher, frames: e.frames, plan: plan, lifetime: lifetime}
	if failed, isRefused := e.dispatcher.carry(lifetime, made.reside).(_Refused); isRefused {
		return nil, failed.err
	}
	return made, nil
}

func (u *_RemoteUnit) reside(e *_Errand, l *_Life) _Outcome {
	return l.reside(u, e.report).result
}

func (u *_RemoteUnit) request() (*_RequestBody, error) {
	return writePlan(u.plan, u.frames.limit)
}

func (u *_RemoteUnit) serve(ctx context.Context, ask _Ask) _Outcome {
	written, err := writeWork(ask, u.frames.limit)
	if err != nil {
		return _Refused{err: err}
	}
	return u.dispatcher.carry(ctx, func(e *_Errand, l *_Life) _Outcome {
		for range servingAttempts {
			var r = l.reside(u, e.report)
			if _, built := r.result.(_Answered); !built {
				return r.result
			}
			var served = l.serve(e.ctx, _Outgoing{unit: r.build, body: written.body})
			if failed, isRefused := served.(_Refused); !isRefused || !errors.Is(failed.err, errMissing) {
				return served
			}
			l.drop(r)
		}
		return _Refused{err: errMissing}
	})
}
