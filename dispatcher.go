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

type outcome struct {
	at   string
	data []byte
	err  error
	died bool
}

func (o outcome) failed() bool {
	return o.died || o.err != nil
}

type lease struct {
	child *evaluatorProcess
	alone bool
}

type admission struct {
	lease lease
	err   error
}

type ticket struct {
	ctx     context.Context
	answers chan admission
	held    lease
}

type dispatcher struct {
	limit    int
	spawn    func(ctx context.Context) (*evaluatorProcess, error)
	mu       sync.Mutex
	mode     mode
	child    *evaluatorProcess
	spawning bool
	running  int
	waiting  []*ticket
	suspects []*ticket
}

func newDispatcher(limit int, spawn func(ctx context.Context) (*evaluatorProcess, error), first *evaluatorProcess) *dispatcher {
	d := &dispatcher{limit: limit, spawn: spawn}
	d.adopt(first)
	return d
}

func (d *dispatcher) carry(ctx context.Context, attempt func(ctx context.Context, child *evaluatorProcess) outcome) outcome {
	t := &ticket{ctx: ctx, answers: make(chan admission, 1)}
	d.mu.Lock()
	d.waiting = append(d.waiting, t)
	d.dispatch()
	d.mu.Unlock()
	for {
		if err := d.await(t); err != nil {
			return outcome{err: err}
		}
		result := attempt(ctx, t.held.child)
		if result.died && result.err == nil && t.held.alone {
			result.err = t.held.child.deathCause()
		}
		if !d.release(t, result) {
			return result
		}
	}
}

func (d *dispatcher) current() *evaluatorProcess {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.child
}

func (d *dispatcher) await(t *ticket) error {
	select {
	case admitted := <-t.answers:
		t.held = admitted.lease
		if admitted.err != nil || t.ctx.Err() == nil {
			return admitted.err
		}
		d.release(t, outcome{})
		return cancelled(t.ctx)
	case <-t.ctx.Done():
	}
	d.mu.Lock()
	withdrawn := d.withdraw(t)
	d.mu.Unlock()
	if !withdrawn {
		if admitted := <-t.answers; admitted.err == nil {
			t.held = admitted.lease
			d.release(t, outcome{})
		}
	}
	return cancelled(t.ctx)
}

func (d *dispatcher) release(t *ticket, result outcome) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if result.died {
		d.lose(t.held.child)
	}
	d.running--
	replay := result.died && result.err == nil && !t.held.alone
	if replay {
		d.suspects = append(d.suspects, t)
	}
	d.dispatch()
	return replay
}

func (d *dispatcher) withdraw(t *ticket) bool {
	for _, queue := range []*[]*ticket{&d.waiting, &d.suspects} {
		if index := slices.Index(*queue, t); index >= 0 {
			*queue = slices.Delete(*queue, index, index+1)
			d.dispatch()
			return true
		}
	}
	return false
}

func (d *dispatcher) lose(child *evaluatorProcess) {
	if d.child != child {
		return
	}
	d.child = nil
	if d.running > 0 {
		d.mode = recovering
	}
}

func (d *dispatcher) adopt(child *evaluatorProcess) {
	d.child = child
	go func() {
		<-child.dead
		d.mu.Lock()
		defer d.mu.Unlock()
		d.lose(child)
		d.dispatch()
	}()
}

func (d *dispatcher) dispatch() {
	for {
		if d.child != nil && !d.child.alive() {
			d.lose(d.child)
		}
		if d.mode == recovering && d.running == 0 && len(d.suspects) == 0 {
			d.mode = sharing
		}
		queue := d.eligible()
		if queue == nil {
			return
		}
		if d.child == nil {
			if !d.spawning {
				d.spawning = true
				go d.respawn((*queue)[0])
			}
			return
		}
		next := (*queue)[0]
		*queue = (*queue)[1:]
		d.running++
		next.answers <- admission{lease: lease{child: d.child, alone: d.mode == recovering}}
	}
}

func (d *dispatcher) eligible() *[]*ticket {
	switch {
	case d.mode == recovering && d.running == 0:
		return &d.suspects
	case d.mode == sharing && d.running < d.limit && len(d.waiting) > 0:
		return &d.waiting
	}
	return nil
}

func (d *dispatcher) respawn(first *ticket) {
	child, err := d.spawn(first.ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.spawning = false
	switch {
	case err == nil:
		d.adopt(child)
	case d.withdraw(first):
		first.answers <- admission{err: err}
	}
	d.dispatch()
}
