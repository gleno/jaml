package jaml

import (
	"context"
	"slices"
	"sync"
)

type _Mode int

const (
	sharing _Mode = iota
	recovering
)

type _Lost struct{ cause error }

func (_Lost) isOutcome() {}

func (l _Lost) attributed() bool {
	_, stuck := l.cause.(_StuckDeath)
	return stuck
}

type _Availability interface{ isAvailability() }

type _Absent struct{}

type _Starting struct{}

type _Ready struct{ life *_Life }

func (_Absent) isAvailability()   {}
func (_Starting) isAvailability() {}
func (_Ready) isAvailability()    {}

type _Lease interface{ held() *_Life }

type _Shared struct{ life *_Life }

type _Owned struct{ life *_Life }

func (s _Shared) held() *_Life { return s.life }
func (o _Owned) held() *_Life  { return o.life }

type _Admission struct {
	lease _Lease
	err   error
}

type _Errand struct {
	ctx     context.Context
	answers chan _Admission
	reports chan _Outcome
}

type _Attempt func(e *_Errand, l *_Life) _Outcome

type _Dispatcher struct {
	limit    int
	start    func() (*_Life, error)
	mu       sync.Mutex
	mode     _Mode
	child    _Availability
	running  int
	waiting  []*_Errand
	suspects []*_Errand
}

func newDispatcher(limit int, start func() (*_Life, error), first *_Life) *_Dispatcher {
	var d = &_Dispatcher{limit: limit, start: start}
	d.adopt(first)
	return d
}

func (d *_Dispatcher) carry(ctx context.Context, work _Attempt) _Outcome {
	var e = &_Errand{ctx: ctx, answers: make(chan _Admission, 1), reports: make(chan _Outcome, 1)}
	d.mu.Lock()
	d.waiting = append(d.waiting, e)
	d.dispatch()
	d.mu.Unlock()
	go d.run(e, work)
	select {
	case result := <-e.reports:
		return result
	case <-ctx.Done():
		return _Refused{err: cancelled(ctx)}
	}
}

func (d *_Dispatcher) run(e *_Errand, work _Attempt) {
	var requeued = false
	for {
		held, err := d.admit(e)
		if err != nil {
			e.report(_Refused{err: err})
			return
		}
		final, replay := e.judge(held, work(e, held.held()))
		if replay {
			var shared = !requeued && final.(_Lost).attributed()
			requeued = requeued || shared
			d.requeue(e, shared)
			continue
		}
		e.report(final)
		d.release(held)
		return
	}
}

func (e *_Errand) report(result _Outcome) {
	select {
	case e.reports <- result:
	default:
	}
}

func (e *_Errand) judge(held _Lease, result _Outcome) (_Outcome, bool) {
	died, isLost := result.(_Lost)
	switch {
	case !isLost:
		return result, false
	case e.ctx.Err() != nil:
		return _Refused{err: cancelled(e.ctx)}, false
	}
	if _, alone := held.(_Owned); alone {
		return _Refused{err: blameless(died.cause)}, false
	}
	return result, true
}

func blameless(cause error) error {
	if outside, killed := cause.(_OutsideDeath); killed {
		return failWith(unavailable, outside)
	}
	return cause
}

func (d *_Dispatcher) admit(e *_Errand) (_Lease, error) {
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
	var withdrawn = d.withdraw(e)
	d.mu.Unlock()
	if !withdrawn {
		if admitted := <-e.answers; admitted.err == nil {
			d.release(admitted.lease)
		}
	}
	return nil, cancelled(e.ctx)
}

func (d *_Dispatcher) release(held _Lease) {
	if alone, isOwned := held.(_Owned); isOwned {
		alone.life.kill(errRetired)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running--
	d.dispatch()
}

func (d *_Dispatcher) requeue(e *_Errand, shared bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running--
	if shared {
		d.waiting = slices.Insert(d.waiting, 0, e)
	} else {
		d.mode = recovering
		d.suspects = append(d.suspects, e)
	}
	d.dispatch()
}

func (d *_Dispatcher) withdraw(e *_Errand) bool {
	for _, queue := range []*[]*_Errand{&d.waiting, &d.suspects} {
		if index := slices.Index(*queue, e); index >= 0 {
			*queue = slices.Delete(*queue, index, index+1)
			d.dispatch()
			return true
		}
	}
	return false
}

func (d *_Dispatcher) adopt(l *_Life) {
	d.child = _Ready{life: l}
	go func() {
		<-l.ended
		d.mu.Lock()
		defer d.mu.Unlock()
		d.lose(l)
		d.dispatch()
	}()
}

func (d *_Dispatcher) lose(l *_Life) {
	if d.child != (_Ready{life: l}) {
		return
	}
	d.child = _Absent{}
	if d.running > 0 && !l.lost().attributed() {
		d.mode = recovering
	}
}

func (d *_Dispatcher) dispatch() {
	for {
		if current, isReady := d.child.(_Ready); isReady && !current.life.alive() {
			d.lose(current.life)
		}
		if d.mode == recovering && d.running == 0 && len(d.suspects) == 0 {
			d.mode = sharing
		}
		var queue = d.eligible()
		if queue == nil {
			return
		}
		current, isReady := d.child.(_Ready)
		if !isReady {
			if d.child == (_Absent{}) {
				d.child = _Starting{}
				go d.begin()
			}
			return
		}
		var next = (*queue)[0]
		*queue = (*queue)[1:]
		d.running++
		next.answers <- _Admission{lease: d.lend(current.life)}
	}
}

func (d *_Dispatcher) lend(l *_Life) _Lease {
	if d.mode == sharing {
		return _Shared{life: l}
	}
	d.child = _Absent{}
	return _Owned{life: l}
}

func (d *_Dispatcher) eligible() *[]*_Errand {
	switch {
	case d.mode == recovering && d.running == 0:
		return &d.suspects
	case d.mode == sharing && d.running < d.limit && len(d.waiting) > 0:
		return &d.waiting
	}
	return nil
}

func (d *_Dispatcher) begin() {
	started, err := d.start()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.child = _Absent{}
		d.refuseEveryone(err)
	} else {
		d.adopt(started)
	}
	d.dispatch()
}

func (d *_Dispatcher) refuseEveryone(err error) {
	for _, e := range slices.Concat(d.waiting, d.suspects) {
		e.answers <- _Admission{err: err}
	}
	d.waiting, d.suspects = nil, nil
}
