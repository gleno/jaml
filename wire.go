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

type _Wire struct {
	writer    chan struct{}
	requests  *os.File
	sent      uint64
	control   sync.Mutex
	controls  *os.File
	mu        sync.Mutex
	residents map[*_RemoteUnit]*_Residency
}

type _Residency struct {
	unit    *_RemoteUnit
	build   uint64
	settled chan struct{}
	result  _Outcome
	unbind  func() bool
}

type _Switchboard struct {
	mu      sync.Mutex
	pending map[uint64]*_Expectation
}

type _Expectation struct {
	op      _Opcode
	replies chan _Reply
}

type _Call struct {
	job     uint64
	replies <-chan _Reply
}

func newWire(requests, controls *os.File) *_Wire {
	return &_Wire{writer: make(chan struct{}, 1), requests: requests, controls: controls, residents: map[*_RemoteUnit]*_Residency{}}
}

func settledWith(result _Outcome) *_Residency {
	settled := make(chan struct{})
	close(settled)
	return &_Residency{settled: settled, result: result}
}

func (l *_Life) reside(u *_RemoteUnit, early func(_Outcome)) *_Residency {
	r, mine := l.lodge(u)
	if mine {
		l.build(r, early)
	}
	<-r.settled
	return r
}

func (l *_Life) lodge(u *_RemoteUnit) (*_Residency, bool) {
	l.wire.mu.Lock()
	defer l.wire.mu.Unlock()
	switch {
	case l.wire.residents[u] != nil:
		return l.wire.residents[u], false
	case u.lifetime.Err() != nil:
		return settledWith(_Refused{err: errLifetimeOver}), false
	case !l.alive():
		return settledWith(l.lost()), false
	}
	r := &_Residency{unit: u, settled: make(chan struct{})}
	l.wire.residents[u] = r
	r.unbind = context.AfterFunc(u.lifetime, func() { l.evict(r) })
	return r, true
}

func (l *_Life) build(r *_Residency, early func(_Outcome)) {
	race := raceBuild(context.Background(), l.buildTimeout)
	plan := r.unit.plan
	asked, err := l.send(race.bound, _Request{Op: opBuild, Plan: &plan})
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
				l.tell(_Request{Op: opForget, Unit: asked.job})
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
			delivered = l.tell(_Request{Op: opForget, Unit: asked.job})
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

func builtFrom(incoming _Reply) _Outcome {
	if incoming.Failure != nil {
		return _Refused{err: incoming.Failure.rebuild()}
	}
	return _Answered{}
}

func (l *_Life) settle(r *_Residency, result _Outcome) {
	select {
	case <-r.settled:
		return
	default:
	}
	if _, built := result.(_Answered); !built {
		l.drop(r)
	}
	r.result = result
	close(r.settled)
}

func (l *_Life) claim(r *_Residency, build uint64) {
	l.wire.mu.Lock()
	resident := l.wire.residents[r.unit] == r
	if resident {
		r.build = build
	}
	l.wire.mu.Unlock()
	if !resident {
		l.tell(_Request{Op: opForget, Unit: build})
	}
}

func (l *_Life) evict(r *_Residency) {
	if build := l.drop(r); build != 0 {
		l.tell(_Request{Op: opForget, Unit: build})
	}
}

func (l *_Life) drop(r *_Residency) uint64 {
	l.wire.mu.Lock()
	defer l.wire.mu.Unlock()
	if l.wire.residents[r.unit] != r {
		return 0
	}
	delete(l.wire.residents, r.unit)
	r.unbind()
	return r.build
}

func (w *_Wire) vacate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range w.residents {
		r.unbind()
	}
	clear(w.residents)
}

func (l *_Life) serve(ctx context.Context, unit uint64, asked _Work) _Outcome {
	sent, err := l.send(ctx, _Request{Op: opServe, Unit: unit, Work: &asked})
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
			delivered = l.tell(_Request{Op: opCancel, Job: sent.job})
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

func servedFrom(incoming _Reply) _Outcome {
	switch {
	case incoming.Failure != nil:
		return _Refused{err: incoming.Failure.rebuild()}
	case incoming.Missing:
		return _Refused{err: errMissing}
	}
	return _Answered{data: incoming.Data, skipped: incoming.Skipped}
}

func (l *_Life) unsent(err error) _Outcome {
	if errors.Is(err, errLost) {
		return l.lost()
	}
	return _Refused{err: err}
}

func (l *_Life) send(ctx context.Context, outgoing _Request) (_Call, error) {
	if ctx.Err() != nil {
		return _Call{}, cancelled(ctx)
	}
	select {
	case l.wire.writer <- struct{}{}:
	case <-ctx.Done():
		return _Call{}, cancelled(ctx)
	case <-l.ended:
		return _Call{}, errLost
	}
	defer func() { <-l.wire.writer }()
	outgoing.Job = l.wire.sent + 1
	encoded, err := wireJSON(outgoing)
	if err != nil {
		return _Call{}, fmt.Errorf("writing the evaluator's request: %w", err)
	}
	l.wire.sent = outgoing.Job
	replies := l.board.expect(outgoing.Job, outgoing.Op)
	if _, err := l.wire.requests.Write(append(encoded, '\n')); err != nil {
		l.end()
		return _Call{}, errLost
	}
	return _Call{job: outgoing.Job, replies: replies}, nil
}

func (l *_Life) tell(outgoing _Request) <-chan struct{} {
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

func (l *_Life) listen(replies *_ReplyStream) {
	for {
		var incoming _Reply
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

func (b *_Switchboard) expect(job uint64, op _Opcode) <-chan _Reply {
	replies := make(chan _Reply, 1)
	b.mu.Lock()
	b.pending[job] = &_Expectation{op: op, replies: replies}
	b.mu.Unlock()
	return replies
}

func (b *_Switchboard) deliver(incoming _Reply) error {
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
