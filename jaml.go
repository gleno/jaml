package jaml

import (
	goruntime "runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gleno/fault"
)

var (
	ExpressionError   = fault.Sentinel("expression error")
	InvalidExpression = ExpressionError.WithMessage("invalid expression")
	InvalidTarget     = ExpressionError.WithMessage("invalid target")
	MissingPath       = ExpressionError.WithMessage("missing path")
	EvaluationFailed  = ExpressionError.WithMessage("evaluation failed")
	CoercionFailed    = ExpressionError.WithMessage("coercion failed")
	Closed            = ExpressionError.WithMessage("closed")
	Unsupported       = ExpressionError.WithMessage("unsupported")
	Unavailable       = ExpressionError.WithMessage("unavailable")
	MemoryLimit       = EvaluationFailed.WithMessage("exceeded the memory limit")
)

type Coercion int

const (
	Loose Coercion = iota + 1
	Strict
)

const (
	smallestMemoryLimit = 16 << 20
	largestMemoryLimit  = 1 << 40
	defaultMemoryLimit  = _MemoryLimit(256 << 20)
	defaultBuildTimeout = 5 * time.Second
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
	builtins       _Builtins
}

type _MemoryLimit int64

func makeMemoryLimit(bytes int64) (_MemoryLimit, error) {
	if bytes < smallestMemoryLimit || bytes > largestMemoryLimit {
		return 0, fail(InvalidExpression, "memory limit must be between 16 MiB (%d) and 1 TiB (%d) bytes, got %d", smallestMemoryLimit, largestMemoryLimit, bytes)
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
			return fail(InvalidExpression, "max concurrency must be at least 1, got %d", jobs)
		}
		s.maxConcurrency = jobs
		return nil
	}
}

func WithBuildTimeout(timeout time.Duration) SandboxOption {
	return func(s *_SandboxSettings) error {
		if timeout <= 0 {
			return fail(InvalidExpression, "build timeout must be positive, got %s", timeout)
		}
		s.buildTimeout = timeout
		return nil
	}
}

func withBuiltins(builtins _Builtins) SandboxOption {
	return func(s *_SandboxSettings) error {
		s.builtins = builtins
		return nil
	}
}

func configure(options []Option) (_ExpressionSettings, error) {
	var configured = _ExpressionSettings{null: Loose, undefined: Strict}
	for index, option := range options {
		if option == nil {
			return _ExpressionSettings{}, fail(InvalidExpression, "option %d is nil", index+1)
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
			return _SandboxSettings{}, fail(InvalidExpression, "option %d is nil", index+1)
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
		return fail(InvalidExpression, "%s coercion must be jaml.Loose or jaml.Strict, got %d", name, coercion)
	}
	return nil
}

func checkSigil(sigil string) error {
	switch {
	case sigil == "":
		return fail(InvalidExpression, "a sigil must not be empty")
	case !utf8.ValidString(sigil):
		return fail(InvalidExpression, "a sigil must be valid UTF-8")
	case strings.ContainsAny(sigil, "{}"):
		return fail(InvalidExpression, "a sigil must not hold a brace, got %q", sigil)
	case strings.IndexFunc(sigil, unicode.IsSpace) >= 0:
		return fail(InvalidExpression, "a sigil must not hold whitespace, got %q", sigil)
	}
	return nil
}
