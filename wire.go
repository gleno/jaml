package jaml

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	errLost    = errors.New("the evaluator died before the request was written")
	errMissing = errors.New("the evaluator holds no such unit")
)

type wire struct {
	writer    chan struct{}
	requests  *os.File
	sent      uint64
	control   sync.Mutex
	controls  *os.File
	mu        sync.Mutex
	residents map[*remoteUnit]*residency
}

type residency struct {
	unit    *remoteUnit
	build   uint64
	settled chan struct{}
	result  outcome
	unbind  func() bool
}

type switchboard struct {
	mu      sync.Mutex
	pending map[uint64]*expectation
}

type expectation struct {
	op      opcode
	replies chan reply
}

type call struct {
	job     uint64
	replies <-chan reply
}

func newWire(requests, controls *os.File) *wire {
	return &wire{writer: make(chan struct{}, 1), requests: requests, controls: controls, residents: map[*remoteUnit]*residency{}}
}

func settledWith(result outcome) *residency {
	settled := make(chan struct{})
	close(settled)
	return &residency{settled: settled, result: result}
}

func (l *life) reside(u *remoteUnit, early func(outcome)) *residency {
	r, mine := l.lodge(u)
	if mine {
		l.build(r, early)
	}
	<-r.settled
	return r
}

func (l *life) lodge(u *remoteUnit) (*residency, bool) {
	l.wire.mu.Lock()
	defer l.wire.mu.Unlock()
	switch {
	case l.wire.residents[u] != nil:
		return l.wire.residents[u], false
	case u.lifetime.Err() != nil:
		return settledWith(refused{err: errLifetimeOver}), false
	case !l.alive():
		return settledWith(l.lost()), false
	}
	r := &residency{unit: u, settled: make(chan struct{})}
	l.wire.residents[u] = r
	r.unbind = context.AfterFunc(u.lifetime, func() { l.evict(r) })
	return r, true
}

func (l *life) build(r *residency, early func(outcome)) {
	race := raceBuild(context.Background(), l.buildTimeout)
	plan := r.unit.plan
	asked, err := l.send(race.bound, request{Op: opBuild, Plan: &plan})
	if err != nil {
		race.finish(l.unsent(err))
		l.settle(r, race.await())
		return
	}
	l.claim(r, asked.job)
	overdue := race.done()
	var delivered <-chan struct{}
	var grace <-chan time.Time
	for {
		select {
		case incoming := <-asked.replies:
			if !race.finish(builtFrom(incoming)) && overdue != nil && incoming.Failure == nil {
				l.tell(request{Op: opForget, Unit: asked.job})
			}
			l.settle(r, race.await())
			return
		case <-l.ended:
			race.finish(l.lost())
			l.settle(r, race.await())
			return
		case <-overdue:
			overdue = nil
			l.settle(r, race.await())
			early(race.await())
			delivered = l.tell(request{Op: opForget, Unit: asked.job})
		case <-delivered:
			delivered = nil
			timer := time.NewTimer(cancelGrace)
			defer timer.Stop()
			grace = timer.C
		case <-grace:
			grace = nil
			l.kill(context.Cause(race.bound))
		}
	}
}

func builtFrom(incoming reply) outcome {
	if incoming.Failure != nil {
		return refused{err: incoming.Failure.rebuild()}
	}
	return answered{}
}

func (l *life) settle(r *residency, result outcome) {
	select {
	case <-r.settled:
		return
	default:
	}
	if _, built := result.(answered); !built {
		l.drop(r)
	}
	r.result = result
	close(r.settled)
}

func (l *life) claim(r *residency, build uint64) {
	l.wire.mu.Lock()
	resident := l.wire.residents[r.unit] == r
	if resident {
		r.build = build
	}
	l.wire.mu.Unlock()
	if !resident {
		l.tell(request{Op: opForget, Unit: build})
	}
}

func (l *life) evict(r *residency) {
	if build := l.drop(r); build != 0 {
		l.tell(request{Op: opForget, Unit: build})
	}
}

func (l *life) drop(r *residency) uint64 {
	l.wire.mu.Lock()
	defer l.wire.mu.Unlock()
	if l.wire.residents[r.unit] != r {
		return 0
	}
	delete(l.wire.residents, r.unit)
	r.unbind()
	return r.build
}

func (w *wire) vacate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range w.residents {
		r.unbind()
	}
	clear(w.residents)
}

func (l *life) serve(ctx context.Context, unit uint64, asked work) outcome {
	sent, err := l.send(ctx, request{Op: opServe, Unit: unit, Work: &asked})
	if err != nil {
		return l.unsent(err)
	}
	stopped := ctx.Done()
	var delivered <-chan struct{}
	var grace <-chan time.Time
	for {
		select {
		case incoming := <-sent.replies:
			return servedFrom(incoming)
		case <-l.ended:
			return l.lost()
		case <-stopped:
			stopped = nil
			delivered = l.tell(request{Op: opCancel, Job: sent.job})
		case <-delivered:
			delivered = nil
			timer := time.NewTimer(cancelGrace)
			defer timer.Stop()
			grace = timer.C
		case <-grace:
			grace = nil
			l.kill(cancelled(ctx))
		}
	}
}

func servedFrom(incoming reply) outcome {
	switch {
	case incoming.Failure != nil:
		return refused{err: incoming.Failure.rebuild()}
	case incoming.Missing:
		return refused{err: errMissing}
	}
	return answered{data: incoming.Data, skipped: incoming.Skipped}
}

func (l *life) unsent(err error) outcome {
	if errors.Is(err, errLost) {
		return l.lost()
	}
	return refused{err: err}
}

func (l *life) send(ctx context.Context, outgoing request) (call, error) {
	if ctx.Err() != nil {
		return call{}, cancelled(ctx)
	}
	select {
	case l.wire.writer <- struct{}{}:
	case <-ctx.Done():
		return call{}, cancelled(ctx)
	case <-l.ended:
		return call{}, errLost
	}
	defer func() { <-l.wire.writer }()
	outgoing.Job = l.wire.sent + 1
	encoded, err := wireJSON(outgoing)
	if err != nil {
		return call{}, fmt.Errorf("writing the evaluator's request: %w", err)
	}
	l.wire.sent = outgoing.Job
	replies := l.board.expect(outgoing.Job, outgoing.Op)
	if _, err := l.wire.requests.Write(append(encoded, '\n')); err != nil {
		l.end()
		return call{}, errLost
	}
	return call{job: outgoing.Job, replies: replies}, nil
}

func (l *life) tell(outgoing request) <-chan struct{} {
	delivered := make(chan struct{})
	go func() {
		encoded, err := wireJSON(outgoing)
		if err != nil {
			l.kill(fmt.Errorf("writing the evaluator's control message: %w", err))
			return
		}
		l.wire.control.Lock()
		_, err = l.wire.controls.Write(append(encoded, '\n'))
		l.wire.control.Unlock()
		if err == nil {
			close(delivered)
		}
	}()
	return delivered
}

func (l *life) listen(replies *replyStream) {
	for {
		var incoming reply
		err := replies.read(&incoming)
		if errors.Is(err, errHungUp) {
			return
		}
		if err == nil {
			err = l.board.deliver(incoming)
		}
		if err != nil {
			l.kill(err)
			return
		}
	}
}

func (b *switchboard) expect(job uint64, op opcode) <-chan reply {
	replies := make(chan reply, 1)
	b.mu.Lock()
	b.pending[job] = &expectation{op: op, replies: replies}
	b.mu.Unlock()
	return replies
}

func (b *switchboard) deliver(incoming reply) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	waiting := b.pending[incoming.Job]
	if waiting == nil {
		return fmt.Errorf("the evaluator answered job %d, which is not waiting for an answer", incoming.Job)
	}
	if err := incoming.fits(waiting.op); err != nil {
		return fmt.Errorf("job %d: %w", incoming.Job, err)
	}
	delete(b.pending, incoming.Job)
	waiting.replies <- incoming
	return nil
}
