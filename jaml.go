package jaml

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gleno/fault"
)

type _Kind int

const (
	invalidExpression _Kind = iota
	invalidTarget
	missingPath
	evaluationFailed
	coercionFailed
	closed
	unsupported
	unavailable
	invalidInput
)

type _KindEntry struct {
	name     string
	sentinel fault.Fault
}

var ExpressionError = fault.Sentinel("expression error")

var kinds = [...]_KindEntry{
	invalidExpression: {"InvalidExpression", ExpressionError.WithMessage("invalid expression")},
	invalidTarget:     {"InvalidTarget", ExpressionError.WithMessage("invalid target")},
	missingPath:       {"MissingPath", ExpressionError.WithMessage("missing path")},
	evaluationFailed:  {"EvaluationFailed", ExpressionError.WithMessage("evaluation failed")},
	coercionFailed:    {"CoercionFailed", ExpressionError.WithMessage("coercion failed")},
	closed:            {"Closed", ExpressionError.WithMessage("closed")},
	unsupported:       {"Unsupported", ExpressionError.WithMessage("unsupported")},
	unavailable:       {"Unavailable", ExpressionError.WithMessage("unavailable")},
	invalidInput:      {"InvalidInput", ExpressionError.WithMessage("invalid input")},
}

type _Cause int

const (
	memoryExhausted _Cause = iota
	wallExhausted
	callDepthExhausted
	evaluatorDeath
	timeExhausted
)

var causes = [...]_KindEntry{
	memoryExhausted:    {"MemoryLimit", kinds[evaluationFailed].sentinel.WithMessage("exceeded the memory limit")},
	wallExhausted:      {"WallLimit", kinds[evaluationFailed].sentinel.WithMessage("exceeded the wall time limit")},
	callDepthExhausted: {"CallDepthLimit", kinds[evaluationFailed].sentinel.WithMessage("exceeded the call depth limit")},
	evaluatorDeath:     {"EvaluatorDied", kinds[evaluationFailed].sentinel.WithMessage("evaluator died")},
	timeExhausted:      {"TimeLimit", kinds[evaluationFailed].sentinel.WithMessage("exceeded the time limit")},
}

var (
	InvalidExpression = kinds[invalidExpression].sentinel
	InvalidTarget     = kinds[invalidTarget].sentinel
	MissingPath       = kinds[missingPath].sentinel
	EvaluationFailed  = kinds[evaluationFailed].sentinel
	CoercionFailed    = kinds[coercionFailed].sentinel
	Closed            = kinds[closed].sentinel
	Unsupported       = kinds[unsupported].sentinel
	Unavailable       = kinds[unavailable].sentinel
	InvalidInput      = kinds[invalidInput].sentinel
	MemoryLimit       = causes[memoryExhausted].sentinel
	WallLimit         = causes[wallExhausted].sentinel
	CallDepthLimit    = causes[callDepthExhausted].sentinel
	EvaluatorDied     = causes[evaluatorDeath].sentinel
	TimeLimit         = causes[timeExhausted].sentinel
	KilledFromOutside = kinds[unavailable].sentinel.WithMessage("killed from outside jaml")
	ExitNotCollected  = kinds[evaluationFailed].sentinel.WithMessage("evaluator ended but its exit could not be collected")
	StuckAfterCancel  = kinds[evaluationFailed].sentinel.WithMessage("evaluator stuck in a job after it was cancelled")
)

func (k _Kind) sentinel() fault.Fault {
	return kinds[k].sentinel
}

func causeNamed(name string) (fault.Fault, bool) {
	for _, entry := range causes {
		if entry.name == name {
			return entry.sentinel, true
		}
	}
	return nil, false
}

func causeOf(err error) string {
	for _, entry := range causes {
		if errors.Is(err, entry.sentinel) {
			return entry.name
		}
	}
	return ""
}

func kindNamed(name string) (_Kind, bool) {
	for kind, entry := range kinds {
		if entry.name == name {
			return _Kind(kind), true
		}
	}
	return 0, false
}

type Coercion int

const (
	Loose Coercion = iota + 1
	Strict
)

const (
	smallestMemoryLimit   = 16 << 20
	largestMemoryLimit    = 1 << 40
	defaultMemoryLimit    = _MemoryLimit(256 << 20)
	defaultBuildTimeout   = 5 * time.Second
	defaultCallDepthLimit = 4096
	scriptCacheReserve    = smallestMemoryLimit
	maxCallDepthLimit     = 1 << 15
)

const defaultSigil = "$"

type Option func(*_ExpressionSettings) error

type _ExpressionSettings struct {
	null      Coercion
	undefined Coercion
	function  bool
	sigil     string
}

type SandboxOption func(*_SandboxSettings) error

type _SandboxSettings struct {
	memoryLimit      _MemoryLimit
	maxConcurrency   int
	buildTimeout     time.Duration
	buildTimeLimit   _TimeLimit
	scriptCacheLimit int64
	runtime          _Runtime
	attributedDeaths func(ctx context.Context, death AttributedDeath)
}

type _Runtime struct {
	builtins  _Builtins
	callDepth int
}

func (r _Runtime) depth() int {
	if r.callDepth == 0 {
		return defaultCallDepthLimit
	}
	return r.callDepth
}

type _MemoryLimit int64

func (l _MemoryLimit) exhausted() error {
	return _MemoryExhausted{limit: l}
}

func makeMemoryLimit(bytes int64) (_MemoryLimit, error) {
	if bytes < smallestMemoryLimit || bytes > largestMemoryLimit {
		return 0, fail(invalidExpression, "memory limit must be between 16 MiB (%d) and 1 TiB (%d) bytes, got %d", smallestMemoryLimit, largestMemoryLimit, bytes)
	}
	return _MemoryLimit(bytes), nil
}

func WithNullCoercion(coercion Coercion) Option {
	return func(s *_ExpressionSettings) error {
		s.null = coercion
		return nil
	}
}

func WithUndefinedCoercion(coercion Coercion) Option {
	return func(s *_ExpressionSettings) error {
		s.undefined = coercion
		return nil
	}
}

func AsFunction() Option {
	return func(s *_ExpressionSettings) error {
		s.function = true
		return nil
	}
}

func WithSigil(sigil string) Option {
	return func(s *_ExpressionSettings) error {
		if err := checkSigil(sigil); err != nil {
			return err
		}
		s.sigil = sigil
		return nil
	}
}

func WithMemoryLimit(bytes int64) SandboxOption {
	return func(s *_SandboxSettings) error {
		limit, err := makeMemoryLimit(bytes)
		if err != nil {
			return err
		}
		s.memoryLimit = limit
		return nil
	}
}

func WithMaxConcurrency(jobs int) SandboxOption {
	return func(s *_SandboxSettings) error {
		if jobs < 1 {
			return fail(invalidExpression, "max concurrency must be at least 1, got %d", jobs)
		}
		s.maxConcurrency = jobs
		return nil
	}
}

type AttributedDeath struct {
	Cause error
}

func WithAttributedDeaths(heard func(ctx context.Context, death AttributedDeath)) SandboxOption {
	return func(s *_SandboxSettings) error {
		if heard == nil {
			return fail(invalidExpression, "the listener of attributed deaths must not be nil")
		}
		s.attributedDeaths = heard
		return nil
	}
}

func WithBuildTimeout(timeout time.Duration) SandboxOption {
	return func(s *_SandboxSettings) error {
		if timeout <= 0 {
			return fail(invalidExpression, "build timeout must be positive, got %s", timeout)
		}
		s.buildTimeout = timeout
		return nil
	}
}

func WithBuildTimeLimit(limit time.Duration) SandboxOption {
	return func(s *_SandboxSettings) error {
		if limit <= 0 {
			return fail(invalidExpression, "build time limit must be positive, got %s", limit)
		}
		s.buildTimeLimit = _TimeLimit(limit)
		return nil
	}
}

func WithScriptCacheLimit(bytes int64) SandboxOption {
	return func(s *_SandboxSettings) error {
		if bytes < 1 {
			return fail(invalidExpression, "script cache limit must be positive, got %d", bytes)
		}
		s.scriptCacheLimit = bytes
		return nil
	}
}

func (s _SandboxSettings) childScriptBudget() (int64, error) {
	var limit = s.memoryLimitOrDefault()
	switch {
	case s.scriptCacheLimit == 0:
		return int64(limit) / 8, nil
	case s.scriptCacheLimit > int64(limit)-scriptCacheReserve:
		return 0, fail(invalidExpression, "script cache limit of %d bytes leaves less than %d bytes of the memory limit of %d bytes", s.scriptCacheLimit, scriptCacheReserve, limit)
	}
	return s.scriptCacheLimit, nil
}

func (s _SandboxSettings) trustedScriptBudget() int64 {
	if s.scriptCacheLimit == 0 {
		return trustedScriptBudget
	}
	return s.scriptCacheLimit
}

func WithCallDepthLimit(calls int) SandboxOption {
	return func(s *_SandboxSettings) error {
		limit, err := makeCallDepthLimit(calls)
		if err != nil {
			return err
		}
		s.runtime.callDepth = limit
		return nil
	}
}

func makeCallDepthLimit(calls int) (int, error) {
	if calls < 1 || calls > maxCallDepthLimit {
		return 0, fail(invalidExpression, "call depth limit must be between 1 and %d, got %d", maxCallDepthLimit, calls)
	}
	return calls, nil
}

func withBuiltins(builtins _Builtins) SandboxOption {
	return func(s *_SandboxSettings) error {
		s.runtime.builtins = builtins
		return nil
	}
}

func configure(options []Option) (_ExpressionSettings, error) {
	var configured = _ExpressionSettings{null: Loose, undefined: Strict}
	for index, option := range options {
		if option == nil {
			return _ExpressionSettings{}, fail(invalidExpression, "option %d is nil", index+1)
		}
		if err := option(&configured); err != nil {
			return _ExpressionSettings{}, err
		}
	}
	if err := checkCoercion("null", configured.null); err != nil {
		return _ExpressionSettings{}, err
	}
	if err := checkCoercion("undefined", configured.undefined); err != nil {
		return _ExpressionSettings{}, err
	}
	if configured.sigil == "" {
		configured.sigil = defaultSigil
	}
	return configured, nil
}

func configureSandbox(options []SandboxOption) (_SandboxSettings, error) {
	var configured = _SandboxSettings{maxConcurrency: goruntime.GOMAXPROCS(0), buildTimeout: defaultBuildTimeout}
	for index, option := range options {
		if option == nil {
			return _SandboxSettings{}, fail(invalidExpression, "option %d is nil", index+1)
		}
		if err := option(&configured); err != nil {
			return _SandboxSettings{}, err
		}
	}
	return configured, nil
}

func (s _SandboxSettings) memoryLimitOrDefault() _MemoryLimit {
	if s.memoryLimit == 0 {
		return defaultMemoryLimit
	}
	return s.memoryLimit
}

func checkCoercion(name string, coercion Coercion) error {
	if coercion != Loose && coercion != Strict {
		return fail(invalidExpression, "%s coercion must be jaml.Loose or jaml.Strict, got %d", name, coercion)
	}
	return nil
}

func checkSigil(sigil string) error {
	switch {
	case sigil == "":
		return fail(invalidExpression, "a sigil must not be empty")
	case !utf8.ValidString(sigil):
		return fail(invalidExpression, "a sigil must be valid UTF-8")
	case strings.ContainsAny(sigil, "{}"):
		return fail(invalidExpression, "a sigil must not hold a brace, got %q", sigil)
	case strings.IndexFunc(sigil, unicode.IsSpace) >= 0:
		return fail(invalidExpression, "a sigil must not hold whitespace, got %q", sigil)
	}
	return nil
}
