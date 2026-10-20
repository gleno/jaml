package jaml

import (
	"context"
	"errors"
	goruntime "runtime"
	"slices"
	"sync"
)

var (
	errSandboxEnded = errors.New("the sandbox's context ended")
	errUnreachable  = errors.New("dropped without its context ending")
	errLifetimeOver = errors.New("its context ended")
	errUnjudged     = errors.New("the evaluator died under this job and nothing judged it")
)

type _Engine interface {
	build(ctx context.Context, plan _Blueprint) (_Unit, error)
}

type _Unit interface {
	serve(ctx context.Context, ask _Ask) _Outcome
}

type _Outcome interface{ isOutcome() }

type _Answered struct {
	data    []byte
	skipped [][]string
}

type _Built struct {
	outline *_CodeOutline
}

type _Refused struct{ err error }

func (_Built) isOutcome()    {}
func (_Answered) isOutcome() {}
func (_Refused) isOutcome()  {}

type Sandbox struct {
	lifetime context.Context
	engine   _Engine
}

type Expression struct {
	held *_Handle
	path []string
}

type _Handle struct {
	lifetime context.Context
	keep     *_Keep
}

type _Keep struct {
	sandbox  context.Context
	lifetime context.Context
	end      context.CancelCauseFunc
	mu       sync.Mutex
	holders  int
	unit     _Unit
}

func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error) {
	return makeSecureSandbox(ctx, slices.Concat(options, []SandboxOption{withBuiltins(frozenBuiltins)}))
}

func MakeSecureSandboxWithoutBuiltinsProtection(ctx context.Context, options ...SandboxOption) (*Sandbox, error) {
	return makeSecureSandbox(ctx, slices.Concat(options, []SandboxOption{withBuiltins(mutableBuiltins)}))
}

func makeSecureSandbox(ctx context.Context, options []SandboxOption) (*Sandbox, error) {
	made, err := startSecureSandbox(ctx, options)
	return made, placed(rootPlace, err)
}

func startSecureSandbox(ctx context.Context, options []SandboxOption) (*Sandbox, error) {
	if err := isolationSupported(); err != nil {
		return nil, err
	}
	configured, err := configureSandbox(options)
	if err != nil {
		return nil, err
	}
	isolated, err := startIsolated(ctx, configured)
	if err != nil {
		return nil, err
	}
	return &Sandbox{lifetime: ctx, engine: isolated}, nil
}

func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error) {
	made, err := makeTrustedSandbox(ctx, options)
	return made, placed(rootPlace, err)
}

func makeTrustedSandbox(ctx context.Context, options []SandboxOption) (*Sandbox, error) {
	configured, err := configureSandbox(slices.Concat(options, []SandboxOption{withBuiltins(mutableBuiltins)}))
	if err != nil {
		return nil, err
	}
	if configured.memoryLimit != 0 {
		return nil, fail(invalidExpression, "a trusted sandbox has no memory limit; use MakeSecureSandbox")
	}
	return &Sandbox{lifetime: ctx, engine: newTrusted(configured)}, nil
}

func (s *Sandbox) MakeExpressionFromJaml(ctx context.Context, jaml string, options ...Option) (*Expression, error) {
	made, err := s.fromJaml(ctx, jaml, options)
	return made, placed(rootPlace, err)
}

func (s *Sandbox) fromJaml(ctx context.Context, jaml string, options []Option) (*Expression, error) {
	if s == nil || s.engine == nil {
		return nil, unmadeSandbox()
	}
	if err := admitJamlText(jaml); err != nil {
		return nil, err
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	if configured.function {
		return nil, fail(invalidExpression, "jaml.AsFunction applies to MakeExpression, not to MakeExpressionFromJaml")
	}
	return s.make(ctx, _Blueprint{Written: jaml, Null: configured.null, Undefined: configured.undefined, Sigil: configured.sigil})
}

func (s *Sandbox) MakeExpression(ctx context.Context, expression string, options ...Option) (*Expression, error) {
	made, err := s.fromExpression(ctx, expression, options)
	return made, placed(rootPlace, err)
}

func (s *Sandbox) fromExpression(ctx context.Context, expression string, options []Option) (*Expression, error) {
	if s == nil || s.engine == nil {
		return nil, unmadeSandbox()
	}
	if err := admitSource(expression); err != nil {
		return nil, err
	}
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	if configured.sigil != defaultSigil {
		return nil, fail(invalidExpression, "jaml.WithSigil applies to MakeExpressionFromJaml, not to MakeExpression")
	}
	return s.make(ctx, _Blueprint{Written: expression, Null: configured.null, Undefined: configured.undefined, Raw: true, Function: configured.function})
}

func (s *Sandbox) make(ctx context.Context, plan _Blueprint) (*Expression, error) {
	if failure := s.framed(ctx, nil); failure != nil {
		return nil, failure
	}
	var keep = keepWithin(s.lifetime)
	held, holding := keep.hold(ctx)
	if !holding {
		return nil, keep.closed()
	}
	built, err := s.engine.build(keep.lifetime, plan)
	if failure := s.framed(ctx, err); failure != nil {
		keep.end(failure)
		return nil, failure
	}
	keep.unit = built
	return &Expression{held: held}, nil
}

func keepWithin(sandbox context.Context) *_Keep {
	lifetime, end := context.WithCancelCause(context.Background())
	var unfollow = context.AfterFunc(sandbox, func() { end(errSandboxEnded) })
	context.AfterFunc(lifetime, func() { unfollow() })
	return &_Keep{sandbox: sandbox, lifetime: lifetime, end: end}
}

func (k *_Keep) hold(ctx context.Context) (*_Handle, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ended() {
		return nil, false
	}
	k.holders++
	lifetime, end := context.WithCancelCause(ctx)
	var unfollow = context.AfterFunc(k.lifetime, func() { end(context.Cause(k.lifetime)) })
	context.AfterFunc(lifetime, func() {
		unfollow()
		k.release()
	})
	var held = &_Handle{lifetime: lifetime, keep: k}
	goruntime.AddCleanup(held, func(end context.CancelCauseFunc) { end(errUnreachable) }, end)
	return held, true
}

func (k *_Keep) ended() bool {
	return k.lifetime.Err() != nil || k.sandbox.Err() != nil
}

func (h *_Handle) ended() bool {
	return h.lifetime.Err() != nil || h.keep.ended()
}

func (k *_Keep) sandboxEnded() bool {
	return k.sandbox.Err() != nil || errors.Is(context.Cause(k.lifetime), errSandboxEnded)
}

func (k *_Keep) closed() _Unplaced {
	return fail(closed, "%s has ended", k.endedContext())
}

func (k *_Keep) endedContext() string {
	if k.sandboxEnded() {
		return "the sandbox's context"
	}
	return "every context it was made or copied with"
}

func (h *_Handle) endedContext() string {
	if h.lifetime.Err() != nil && !h.keep.sandboxEnded() {
		return "its own context, the one it was made or copied with,"
	}
	return h.keep.endedContext()
}

func (k *_Keep) release() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.holders--
	if k.holders == 0 {
		k.end(errLifetimeOver)
	}
}

func (s *Sandbox) framed(ctx context.Context, err error) error {
	switch {
	case s.lifetime.Err() != nil:
		return fail(closed, "the sandbox's context has ended")
	case ctx.Err() != nil:
		return _Unplaced{kind: evaluationFailed, label: "building", cause: cancelled(ctx)}
	case err != nil:
		return err
	}
	return nil
}

func unmadeSandbox() error {
	return fail(invalidExpression, "a sandbox not made by MakeSecureSandbox, MakeSecureSandboxWithoutBuiltinsProtection or MakeTrustedSandbox").at(rootPlace)
}

func (e *Expression) At(path ...string) *Expression {
	if e == nil {
		return nil
	}
	return &Expression{held: e.held, path: slices.Concat(e.path, path)}
}

func (e *Expression) Copy(ctx context.Context) (*Expression, error) {
	copied, err := e.copy(ctx)
	return copied, placed(e.selection(), err)
}

func (e *Expression) copy(ctx context.Context) (*Expression, error) {
	if e == nil || e.held == nil {
		return nil, unmadeExpression("Copy")
	}
	if err := e.held.admit(ctx); err != nil {
		return nil, err
	}
	held, holding := e.held.keep.hold(ctx)
	if !holding {
		return nil, e.held.keep.closed()
	}
	return &Expression{held: held, path: e.path}, nil
}

func (e *Expression) selection() string {
	if e == nil {
		return rootPlace
	}
	return selectedPath(e.path).String()
}

func placed(path string, err error) error {
	if err == nil {
		return nil
	}
	return located(path, err)
}

func unmadeExpression(method string) error {
	return fail(invalidExpression, "%s on an expression not made by a Sandbox", method).at(rootPlace)
}

func (h *_Handle) admit(ctx context.Context) error {
	if h.ended() {
		return fail(closed, "%s has ended", h.endedContext())
	}
	if ctx.Err() != nil {
		return cancelled(ctx)
	}
	return nil
}

func (h *_Handle) answer(ctx context.Context, ask _Ask, decode func(settled _Answered) error) error {
	defer goruntime.KeepAlive(h)
	ask.pinned = pin(ctx)
	ask.console = consoleFor(ctx, ask.path)
	defer ask.console.finish()
	var job = jobWithin(ctx, h.lifetime)
	defer job.end()
	var result = h.keep.unit.serve(job, ask)
	switch {
	case h.ended():
		return fail(closed, "%s ended while it ran", h.endedContext())
	case ctx.Err() != nil:
		return cancelled(ctx)
	}
	switch settled := result.(type) {
	case _Answered:
		if err := decode(settled); err != nil {
			return failWith(coercionFailed, err).at(blame(selectedPath(ask.path).String(), settled.data, err))
		}
		return nil
	case _Refused:
		return settled.err
	}
	return errUnjudged
}

type _Job struct {
	context.Context
	lifetime context.Context
	stop     context.CancelCauseFunc
	follow   sync.Once
	unfollow func() bool
}

func jobWithin(ctx context.Context, lifetime context.Context) *_Job {
	job, stop := context.WithCancelCause(ctx)
	return &_Job{Context: job, lifetime: lifetime, stop: stop}
}

func (j *_Job) Done() <-chan struct{} {
	j.follow.Do(func() {
		j.unfollow = context.AfterFunc(j.lifetime, func() { j.stop(errLifetimeOver) })
	})
	return j.Context.Done()
}

func (j *_Job) end() {
	j.follow.Do(func() {})
	if j.unfollow != nil {
		j.unfollow()
	}
	j.stop(nil)
}
