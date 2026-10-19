package jaml

import (
	"context"
	"slices"
	"sync"
)

type mode int

const (
	sharing mode = iota
	recovering
)

type lost struct{ cause error }

func (lost) isOutcome() {}

type availability interface{ isAvailability() }

type absent struct{}

type starting struct{}

type ready struct{ life *life }

func (absent) isAvailability()   {}
func (starting) isAvailability() {}
func (ready) isAvailability()    {}

type lease interface{ held() *life }

type shared struct{ life *life }

type owned struct{ life *life }

func (s shared) held() *life { return s.life }
func (o owned) held() *life  { return o.life }

type admission struct {
	lease lease
	err   error
}

type errand struct {
	ctx     context.Context
	answers chan admission
	reports chan outcome
}

type attempt func(e *errand, l *life) outcome

type dispatcher struct {
	limit    int
	start    func() (*life, error)
	mu       sync.Mutex
	mode     mode
	child    availability
	running  int
	waiting  []*errand
	suspects []*errand
}

func newDispatcher(limit int, start func() (*life, error), first *life) *dispatcher {
	d := &dispatcher{limit: limit, start: start}
	d.adopt(first)
	return d
}

func (d *dispatcher) carry(ctx context.Context, work attempt) outcome {
	e := &errand{ctx: ctx, answers: make(chan admission, 1), reports: make(chan outcome, 1)}
	d.mu.Lock()
	d.waiting = append(d.waiting, e)
	d.dispatch()
	d.mu.Unlock()
	go d.run(e, work)
	select {
	case result := <-e.reports:
		return result
	case <-ctx.Done():
		return refused{err: cancelled(ctx)}
	}
}

func (d *dispatcher) run(e *errand, work attempt) {
	for {
		held, err := d.admit(e)
		if err != nil {
			e.report(refused{err: err})
			return
		}
		final, replay := e.judge(held, work(e, held.held()))
		if replay {
			d.suspect(e)
			continue
		}
		e.report(final)
		d.release(held)
		return
	}
}

func (e *errand) report(result outcome) {
	select {
	case e.reports <- result:
	default:
	}
}

func (e *errand) judge(held lease, result outcome) (outcome, bool) {
	died, isLost := result.(lost)
	switch {
	case !isLost:
		return result, false
	case e.ctx.Err() != nil:
		return refused{err: cancelled(e.ctx)}, false
	}
	if _, alone := held.(owned); alone {
		return refused{err: died.cause}, false
	}
	return result, true
}

func (d *dispatcher) admit(e *errand) (lease, error) {
	select {
	case admitted := <-e.answers:
		if admitted.err != nil || e.ctx.Err() == nil {
			return admitted.lease, admitted.err
		}
		d.release(admitted.lease)
		return nil, cancelled(e.ctx)
	case <-e.ctx.Done():
	}
	d.mu.Lock()
	withdrawn := d.withdraw(e)
	d.mu.Unlock()
	if !withdrawn {
		if admitted := <-e.answers; admitted.err == nil {
			d.release(admitted.lease)
		}
	}
	return nil, cancelled(e.ctx)
}

func (d *dispatcher) release(held lease) {
	if alone, isOwned := held.(owned); isOwned {
		alone.life.kill(errRetired)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running--
	d.dispatch()
}

func (d *dispatcher) suspect(e *errand) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running--
	d.mode = recovering
	d.suspects = append(d.suspects, e)
	d.dispatch()
}

func (d *dispatcher) withdraw(e *errand) bool {
	for _, queue := range []*[]*errand{&d.waiting, &d.suspects} {
		if index := slices.Index(*queue, e); index >= 0 {
			*queue = slices.Delete(*queue, index, index+1)
			d.dispatch()
			return true
		}
	}
	return false
}

func (d *dispatcher) adopt(l *life) {
	d.child = ready{life: l}
	go func() {
		<-l.ended
		d.mu.Lock()
		defer d.mu.Unlock()
		d.lose(l)
		d.dispatch()
	}()
}

func (d *dispatcher) lose(l *life) {
	if d.child != (ready{life: l}) {
		return
	}
	d.child = absent{}
	if d.running > 0 {
		d.mode = recovering
	}
}

func (d *dispatcher) dispatch() {
	for {
		if current, isReady := d.child.(ready); isReady && !current.life.alive() {
			d.lose(current.life)
		}
		if d.mode == recovering && d.running == 0 && len(d.suspects) == 0 {
			d.mode = sharing
		}
		queue := d.eligible()
		if queue == nil {
			return
		}
		current, isReady := d.child.(ready)
		if !isReady {
			if d.child == (absent{}) {
				d.child = starting{}
				go d.begin()
			}
			return
		}
		next := (*queue)[0]
		*queue = (*queue)[1:]
		d.running++
		next.answers <- admission{lease: d.lend(current.life)}
	}
}

func (d *dispatcher) lend(l *life) lease {
	if d.mode == sharing {
		return shared{life: l}
	}
	d.child = absent{}
	return owned{life: l}
}

func (d *dispatcher) eligible() *[]*errand {
	switch {
	case d.mode == recovering && d.running == 0:
		return &d.suspects
	case d.mode == sharing && d.running < d.limit && len(d.waiting) > 0:
		return &d.waiting
	}
	return nil
}

func (d *dispatcher) begin() {
	started, err := d.start()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.child = absent{}
		d.refuseEveryone(err)
	} else {
		d.adopt(started)
	}
	d.dispatch()
}

func (d *dispatcher) refuseEveryone(err error) {
	for _, e := range slices.Concat(d.waiting, d.suspects) {
		e.answers <- admission{err: err}
	}
	d.waiting, d.suspects = nil, nil
}
