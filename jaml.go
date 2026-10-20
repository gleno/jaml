package jaml

import (
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
	MemoryLimit       = EvaluationFailed.WithMessage("exceeded the memory limit")
	TimeLimit         = EvaluationFailed.WithMessage("exceeded the time limit")
)

func (k _Kind) sentinel() fault.Fault {
	return kinds[k].sentinel
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
	memoryLimit    _MemoryLimit
	maxConcurrency int
	buildTimeout   time.Duration
	runtime        _Runtime
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

func WithBuildTimeout(timeout time.Duration) SandboxOption {
	return func(s *_SandboxSettings) error {
		if timeout <= 0 {
			return fail(invalidExpression, "build timeout must be positive, got %s", timeout)
		}
		s.buildTimeout = timeout
		return nil
	}
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
