package jaml

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type _JobPhase interface{ isJobPhase() }

type _Told struct{ halt bool }

type _Working struct {
	cancel  context.CancelFunc
	dropped bool
	grace   *time.Timer
	last    func()
	meter   *_Meter
}

type _Held struct{ unit _Servable }

func (_Told) isJobPhase()    {}
func (_Working) isJobPhase() {}
func (_Held) isJobPhase()    {}

type _JobTable struct {
	mu      sync.Mutex
	arrived uint64
	jobs    map[uint64]_JobPhase
	stuck   func(job uint64)
}

func newJobTable(stuck func(job uint64)) *_JobTable {
	return &_JobTable{jobs: map[uint64]_JobPhase{}, stuck: stuck}
}

func (t *_JobTable) begin(job uint64) (context.Context, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job <= t.arrived {
		return nil, fmt.Errorf("job %d arrived after job %d", job, t.arrived)
	}
	t.arrived = job
	ctx, cancel := context.WithCancel(context.Background())
	var working = _Working{cancel: cancel}
	if told, early := t.jobs[job].(_Told); early {
		working = t.drop(job, working, told)
	}
	t.jobs[job] = working
	return ctx, nil
}

func (t *_JobTable) cancel(job uint64) {
	t.let(job, _Told{halt: true})
}

func (t *_JobTable) forget(job uint64) {
	t.let(job, _Told{})
}

func (t *_JobTable) let(job uint64, told _Told) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch phase := t.jobs[job].(type) {
	case _Working:
		t.jobs[job] = t.drop(job, phase, told)
	case _Held:
		delete(t.jobs, job)
	case _Told:
		t.jobs[job] = _Told{halt: phase.halt || told.halt}
	case nil:
		if job > t.arrived {
			t.jobs[job] = told
		}
	}
}

func (t *_JobTable) measure(job uint64, meter *_Meter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if working, isWorking := t.jobs[job].(_Working); isWorking {
		working.meter = meter
		t.jobs[job] = working
	}
}

func (t *_JobTable) measured() map[uint64]*_Meter {
	t.mu.Lock()
	defer t.mu.Unlock()
	var meters = map[uint64]*_Meter{}
	for job, phase := range t.jobs {
		if working, isWorking := phase.(_Working); isWorking && working.meter != nil {
			meters[job] = working.meter
		}
	}
	return meters
}

func (t *_JobTable) overrun(job uint64, last func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if working, isWorking := t.jobs[job].(_Working); isWorking {
		working.last = last
		t.jobs[job] = t.drop(job, working, _Told{halt: true})
	}
}

func (t *_JobTable) drop(job uint64, working _Working, told _Told) _Working {
	working.cancel()
	working.dropped = true
	if told.halt && working.grace == nil {
		working.grace = time.AfterFunc(cancelGrace, func() { t.expire(job) })
	}
	return working
}

func (t *_JobTable) expire(job uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if working, isWorking := t.jobs[job].(_Working); isWorking {
		if working.last != nil {
			working.last()
		}
		t.stuck(job)
	}
}

func (t *_JobTable) keep(job uint64, unit _Servable) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.release(job).dropped {
		return false
	}
	t.jobs[job] = _Held{unit: unit}
	return true
}

func (t *_JobTable) settle(job uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.release(job).dropped
}

func (t *_JobTable) release(job uint64) _Working {
	var working = t.jobs[job].(_Working)
	delete(t.jobs, job)
	working.cancel()
	if working.grace != nil {
		working.grace.Stop()
	}
	return working
}

func (t *_JobTable) holding(unit uint64) _Servable {
	t.mu.Lock()
	defer t.mu.Unlock()
	held, isHeld := t.jobs[unit].(_Held)
	if !isHeld {
		return nil
	}
	return held.unit
}
