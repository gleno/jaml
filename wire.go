package jaml

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const requestChunk = 64 << 10

var alreadyDone = closedChannel()

var (
	errLost    = errors.New("the evaluator died before the request was written")
	errMissing = errors.New("the evaluator holds no such unit")
)

type _Wire struct {
	writer    chan struct{}
	requests  *os.File
	stall     *_Quiet
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
	mu       sync.Mutex
	pending  map[uint64]*_Expectation
	consoles map[uint64]*_Console
	hungUp   bool
}

type _Expectation struct {
	op      _Opcode
	replies chan _Reply
}

type _Call struct {
	job     uint64
	replies <-chan _Reply
}

func closedChannel() <-chan struct{} {
	var done = make(chan struct{})
	close(done)
	return done
}

func newWire(requests, controls *os.File) *_Wire {
	return &_Wire{writer: make(chan struct{}, 1), requests: requests, stall: &_Quiet{meaning: errNotReading}, controls: controls, residents: map[*_RemoteUnit]*_Residency{}}
}

func settledWith(result _Outcome) *_Residency {
	var settled = make(chan struct{})
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
	var r = &_Residency{unit: u, settled: make(chan struct{})}
	l.wire.residents[u] = r
	r.unbind = context.AfterFunc(u.lifetime, func() { l.evict(r) })
	return r, true
}

func (l *_Life) build(r *_Residency, early func(_Outcome)) {
	var race = raceBuild(context.Background(), l.buildTimeout)
	body, err := r.unit.request()
	if err != nil {
		race.finish(_Refused{err: err})
		l.settle(r, race.await())
		return
	}
	asked, err := l.send(race.bound, _Outgoing{body: body})
	if err != nil {
		race.finish(l.unsent(err))
		l.settle(r, race.await())
		return
	}
	l.claim(r, asked.job)
	var overdue = race.done()
	for {
		select {
		case incoming := <-asked.replies:
			if !race.finish(builtFrom(incoming)) && overdue != nil && incoming.Failure == nil {
				l.tell(_Request{Op: opForget, Unit: asked.job})
			}
			l.settle(r, race.await())
			return
		case <-l.ended:
			race.finish(l.final(asked, builtFrom))
			l.settle(r, race.await())
			return
		case <-overdue:
			overdue = nil
			l.settle(r, race.await())
			early(race.await())
			defer l.backstop(l.tell(_Request{Op: opCancel, Job: asked.job}), l.quiet)()
		}
	}
}

func builtFrom(incoming _Reply) _Outcome {
	if incoming.Failure != nil {
		return _Refused{err: incoming.Failure.rebuild()}
	}
	return _Built{outline: incoming.Code}
}

func (l *_Life) settle(r *_Residency, result _Outcome) {
	select {
	case <-r.settled:
		return
	default:
	}
	if _, built := result.(_Built); !built {
		l.drop(r)
	}
	r.result = result
	close(r.settled)
}

func (l *_Life) claim(r *_Residency, build uint64) {
	l.wire.mu.Lock()
	var resident = l.wire.residents[r.unit] == r
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

func (l *_Life) serve(ctx context.Context, asked _Outgoing) _Outcome {
	sent, err := l.send(ctx, asked)
	if err != nil {
		return l.unsent(err)
	}
	watched, stop := asked.body.overseen(ctx, l.buildTimeout)
	defer stop()
	var stopped = watched.Done()
	for {
		select {
		case incoming := <-sent.replies:
			return servedFrom(incoming)
		case <-l.ended:
			return overran(watched, l.final(sent, servedFrom))
		case <-stopped:
			stopped = nil
			defer l.backstop(l.halt(ctx, sent.job), l.quiet)()
		}
	}
}

func (l *_Life) halt(ctx context.Context, job uint64) <-chan struct{} {
	if ctx.Err() == nil {
		return alreadyDone
	}
	return l.tell(_Request{Op: opCancel, Job: job})
}

func overran(watched context.Context, result _Outcome) _Outcome {
	exhausted, isExhausted := context.Cause(watched).(_TimeExhausted)
	died, isLost := result.(_Lost)
	if !isExhausted || !isLost || !wedged(died.cause) {
		return result
	}
	return _Refused{err: fmt.Errorf("%w: %s", exhausted, died.cause)}
}

func wedged(verdict error) bool {
	return errors.Is(verdict, errSilent) || errors.Is(verdict, errNotReading)
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

func (l *_Life) send(ctx context.Context, outgoing _Outgoing) (_Call, error) {
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
	var job = l.wire.sent + 1
	l.wire.sent = job
	var replies = l.board.expect(job, outgoing.body.op, outgoing.console)
	defer l.backstop(alreadyDone, l.wire.stall)()
	if err := l.wire.write(outgoing.body.stamp(job, outgoing.unit)); err != nil {
		l.end()
		return _Call{}, errLost
	}
	return _Call{job: job, replies: replies}, nil
}

func (w *_Wire) write(request []byte) error {
	for len(request) > 0 {
		var chunk = request[:min(len(request), requestChunk)]
		w.stall.waiting()
		written, err := w.requests.Write(chunk)
		w.stall.heard()
		if err != nil {
			return err
		}
		request = request[written:]
	}
	return nil
}

func (l *_Life) final(c _Call, judged func(_Reply) _Outcome) _Outcome {
	select {
	case incoming := <-c.replies:
		return judged(incoming)
	default:
		return l.lost()
	}
}

func (l *_Life) tell(outgoing _Request) <-chan struct{} {
	var delivered = make(chan struct{})
	go func() {
		encoded, err := wireJSON(outgoing)
		if err != nil {
			l.kill(fmt.Errorf("writing the evaluator's control message: %w", err))
			return
		}
		l.wire.control.Lock()
		_, err = l.wire.controls.Write(headed(encoded))
		l.wire.control.Unlock()
		if err == nil {
			close(delivered)
		}
	}()
	return delivered
}

func (l *_Life) backstop(told <-chan struct{}, quiet *_Quiet) func() {
	var answered = make(chan struct{})
	go func() {
		select {
		case <-told:
		case <-answered:
			return
		case <-l.ended:
			return
		}
		var since = monotonicNow()
		var timer = time.NewTimer(backstopSilence)
		defer timer.Stop()
		for {
			select {
			case <-answered:
				return
			case <-l.ended:
				return
			case <-timer.C:
			}
			var silence = min(monotonicNow()-since, quiet.lasted(), l.quiet.lasted())
			if silence >= backstopSilence {
				l.kill(quiet.meaning)
				return
			}
			timer.Reset(backstopSilence - silence)
		}
	}()
	return sync.OnceFunc(func() { close(answered) })
}

func (l *_Life) listen(replies *_ReplyStream, greeted chan<- error) {
	defer close(l.drained)
	defer l.board.hangUp()
	var err = replies.greeting()
	greeted <- err
	if err != nil {
		return
	}
	for {
		var incoming _Reply
		var err = replies.read(&incoming)
		if errors.Is(err, errHungUp) {
			return
		}
		if err == nil {
			err = l.board.route(incoming)
		}
		if err != nil {
			l.kill(err)
			return
		}
	}
}

func (b *_Switchboard) expect(job uint64, op _Opcode, console *_Console) <-chan _Reply {
	var replies = make(chan _Reply, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[job] = &_Expectation{op: op, replies: replies}
	if console != nil && !b.hungUp && console.attend() {
		if b.consoles == nil {
			b.consoles = map[uint64]*_Console{}
		}
		b.consoles[job] = console
	}
	return replies
}

func (b *_Switchboard) route(incoming _Reply) error {
	if incoming.Line != nil {
		return b.echo(incoming)
	}
	return b.deliver(incoming)
}

func (b *_Switchboard) echo(incoming _Reply) error {
	switch {
	case incoming.outcomes() != 0 || incoming.Code != nil || incoming.Skipped != nil:
		return fmt.Errorf("job %d: the evaluator sent a console line with an answer", incoming.Job)
	case !incoming.Line.Level.known():
		return fmt.Errorf("job %d: the evaluator sent a console line at an unknown level %d", incoming.Job, incoming.Line.Level)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if console := b.consoles[incoming.Job]; console != nil {
		console.echo(Line(*incoming.Line))
	}
	return nil
}

func (b *_Switchboard) hangUp() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hungUp = true
	for job := range b.consoles {
		b.unheed(job)
	}
}

func (b *_Switchboard) unheed(job uint64) {
	if console := b.consoles[job]; console != nil {
		delete(b.consoles, job)
		console.leave()
	}
}

func (b *_Switchboard) deliver(incoming _Reply) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var waiting = b.pending[incoming.Job]
	if waiting == nil {
		return fmt.Errorf("the evaluator answered job %d, which is not waiting for an answer", incoming.Job)
	}
	if err := incoming.fits(waiting.op); err != nil {
		return fmt.Errorf("job %d: %w", incoming.Job, err)
	}
	delete(b.pending, incoming.Job)
	b.unheed(incoming.Job)
	waiting.replies <- incoming
	return nil
}
