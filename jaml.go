package jaml

import (
	"reflect"
	goruntime "runtime"
	"time"

	"github.com/gleno/fault"
)

var (
	TemplateError    = fault.Sentinel("template error")
	InvalidTemplate  = TemplateError.WithMessage("invalid template")
	InvalidTarget    = TemplateError.WithMessage("invalid target")
	MissingPath      = TemplateError.WithMessage("missing path")
	EvaluationFailed = TemplateError.WithMessage("evaluation failed")
	CoercionFailed   = TemplateError.WithMessage("coercion failed")
	Closed           = TemplateError.WithMessage("closed")
	Unsupported      = TemplateError.WithMessage("unsupported")
	MemoryLimit      = EvaluationFailed.WithMessage("exceeded the memory limit")
)

type Coercion int

const (
	Loose Coercion = iota + 1
	Strict
)

const (
	smallestMemoryLimit = 16 << 20
	largestMemoryLimit  = 1 << 40
	defaultMemoryLimit  = memoryLimit(256 << 20)
	defaultBuildTimeout = 5 * time.Second
)

type Option func(*settings) error

type settings struct {
	null      Coercion
	undefined Coercion
}

type SandboxOption func(*sandboxSettings) error

type sandboxSettings struct {
	memoryLimit    memoryLimit
	maxConcurrency int
	buildTimeout   time.Duration
}

type memoryLimit int64

func makeMemoryLimit(bytes int64) (memoryLimit, error) {
	if bytes < smallestMemoryLimit || bytes > largestMemoryLimit {
		return 0, fail(InvalidTemplate, "memory limit must be between 16 MiB (%d) and 1 TiB (%d) bytes, got %d", smallestMemoryLimit, largestMemoryLimit, bytes)
	}
	return memoryLimit(bytes), nil
}

func WithNullCoercion(coercion Coercion) Option {
	return func(s *settings) error {
		s.null = coercion
		return nil
	}
}

func WithUndefinedCoercion(coercion Coercion) Option {
	return func(s *settings) error {
		s.undefined = coercion
		return nil
	}
}

func WithMemoryLimit(bytes int64) SandboxOption {
	return func(s *sandboxSettings) error {
		limit, err := makeMemoryLimit(bytes)
		if err != nil {
			return err
		}
		s.memoryLimit = limit
		return nil
	}
}

func WithMaxConcurrency(jobs int) SandboxOption {
	return func(s *sandboxSettings) error {
		if jobs < 1 {
			return fail(InvalidTemplate, "max concurrency must be at least 1, got %d", jobs)
		}
		s.maxConcurrency = jobs
		return nil
	}
}

func WithBuildTimeout(timeout time.Duration) SandboxOption {
	return func(s *sandboxSettings) error {
		if timeout <= 0 {
			return fail(InvalidTemplate, "build timeout must be positive, got %s", timeout)
		}
		s.buildTimeout = timeout
		return nil
	}
}

func configure(options []Option) (settings, error) {
	configured := settings{null: Loose, undefined: Strict}
	for index, option := range options {
		if option == nil {
			return settings{}, fail(InvalidTemplate, "option %d is nil", index+1)
		}
		if err := option(&configured); err != nil {
			return settings{}, err
		}
	}
	if err := checkCoercion("null", configured.null); err != nil {
		return settings{}, err
	}
	if err := checkCoercion("undefined", configured.undefined); err != nil {
		return settings{}, err
	}
	return configured, nil
}

func configureSandbox(options []SandboxOption) (sandboxSettings, error) {
	configured := sandboxSettings{maxConcurrency: goruntime.GOMAXPROCS(0), buildTimeout: defaultBuildTimeout}
	for index, option := range options {
		if option == nil {
			return sandboxSettings{}, fail(InvalidTemplate, "option %d is nil", index+1)
		}
		if err := option(&configured); err != nil {
			return sandboxSettings{}, err
		}
	}
	return configured, nil
}

func (s sandboxSettings) memoryLimitOrDefault() memoryLimit {
	if s.memoryLimit == 0 {
		return defaultMemoryLimit
	}
	return s.memoryLimit
}

func checkCoercion(name string, coercion Coercion) error {
	if coercion != Loose && coercion != Strict {
		return fail(InvalidTemplate, "%s coercion must be jaml.Loose or jaml.Strict, got %d", name, coercion)
	}
	return nil
}

func describeTarget(into any) string {
	if into == nil {
		return "nil"
	}
	target := reflect.ValueOf(into)
	if target.Kind() == reflect.Pointer && target.IsNil() {
		return "nil " + target.Type().String()
	}
	return target.Type().String()
}
