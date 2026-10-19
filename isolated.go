package jaml

import (
	"context"
	"sync/atomic"
)

type isolatedEngine struct {
	dispatcher *dispatcher
	units      atomic.Uint64
}

type unitRef struct {
	id   uint64
	plan blueprint
}

type remoteUnit struct {
	engine *isolatedEngine
	ref    unitRef
}

func startIsolated(lifetime context.Context, configured sandboxSettings) (*isolatedEngine, error) {
	spec := childSpec{lifetime: lifetime, limit: configured.memoryLimitOrDefault(), buildTimeout: configured.buildTimeout}
	first, err := spawnEvaluator(lifetime, spec)
	if err != nil {
		return nil, err
	}
	spawn := func(ctx context.Context) (*evaluatorProcess, error) { return spawnEvaluator(ctx, spec) }
	return &isolatedEngine{dispatcher: newDispatcher(configured.maxConcurrency, spawn, first)}, nil
}

func (e *isolatedEngine) build(ctx context.Context, plan blueprint) (unit, error) {
	ref := unitRef{id: e.units.Add(1), plan: plan}
	result := e.dispatcher.carry(ctx, func(ctx context.Context, child *evaluatorProcess) outcome {
		return child.ensureBuilt(ctx, ref)
	})
	if result.err != nil {
		return nil, result.err
	}
	return &remoteUnit{engine: e, ref: ref}, nil
}

func (u *remoteUnit) serve(ctx context.Context, asked work) outcome {
	return u.engine.dispatcher.carry(ctx, func(ctx context.Context, child *evaluatorProcess) outcome {
		if built := child.ensureBuilt(ctx, u.ref); built.failed() {
			return built
		}
		return child.serve(ctx, request{Op: opServe, Unit: u.ref.id, Work: &asked})
	})
}

func (u *remoteUnit) forget() {
	if child := u.engine.dispatcher.current(); child != nil {
		child.forget(u.ref.id)
	}
}
