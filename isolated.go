package jaml

import (
	"context"
	"errors"
)

const servingAttempts = 2

type isolatedEngine struct {
	dispatcher *dispatcher
}

type remoteUnit struct {
	dispatcher *dispatcher
	plan       blueprint
	lifetime   context.Context
}

func startIsolated(lifetime context.Context, configured sandboxSettings) (*isolatedEngine, error) {
	spec := childSpec{lifetime: lifetime, limit: configured.memoryLimitOrDefault(), buildTimeout: configured.buildTimeout}
	first, err := startLife(spec)
	if err != nil {
		return nil, err
	}
	start := func() (*life, error) { return startLife(spec) }
	return &isolatedEngine{dispatcher: newDispatcher(configured.maxConcurrency, start, first)}, nil
}

func (e *isolatedEngine) build(lifetime context.Context, plan blueprint) (unit, error) {
	made := &remoteUnit{dispatcher: e.dispatcher, plan: plan, lifetime: lifetime}
	if failed, isRefused := e.dispatcher.carry(lifetime, made.reside).(refused); isRefused {
		return nil, failed.err
	}
	return made, nil
}

func (u *remoteUnit) reside(e *errand, l *life) outcome {
	return l.reside(u, e.report).result
}

func (u *remoteUnit) serve(ctx context.Context, asked work) outcome {
	return u.dispatcher.carry(ctx, func(e *errand, l *life) outcome {
		for range servingAttempts {
			r := l.reside(u, e.report)
			if _, built := r.result.(answered); !built {
				return r.result
			}
			served := l.serve(e.ctx, r.build, asked)
			if failed, isRefused := served.(refused); !isRefused || !errors.Is(failed.err, errMissing) {
				return served
			}
			l.drop(r)
		}
		return refused{err: errMissing}
	})
}
