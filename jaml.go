package jaml

import (
	"fmt"
	"reflect"

	"github.com/gleno/fault"
)

var (
	TemplateError    = fault.Sentinel("template error")
	InvalidTemplate  = TemplateError.WithMessage("invalid template")
	InvalidTarget    = TemplateError.WithMessage("invalid target")
	MissingPath      = TemplateError.WithMessage("missing path")
	EvaluationFailed = TemplateError.WithMessage("evaluation failed")
	CoercionFailed   = TemplateError.WithMessage("coercion failed")
	Closed           = TemplateError.WithMessage("template closed")
	Unsupported      = TemplateError.WithMessage("unsupported")
)

type Coercion int

const (
	Loose Coercion = iota + 1
	Strict
)

const defaultMemoryLimit = 256 << 20

type Option func(*settings)

type settings struct {
	null        Coercion
	undefined   Coercion
	memoryLimit int64
}

func WithNullCoercion(coercion Coercion) Option {
	return func(s *settings) { s.null = coercion }
}

func WithUndefinedCoercion(coercion Coercion) Option {
	return func(s *settings) { s.undefined = coercion }
}

func WithMemoryLimit(bytes int64) Option {
	return func(s *settings) { s.memoryLimit = bytes }
}

func configure(options []Option) (settings, error) {
	configured := settings{null: Loose, undefined: Strict}
	for index, option := range options {
		if option == nil {
			return settings{}, fault.From(InvalidTemplate, fmt.Sprintf("option %d is nil", index+1))
		}
		option(&configured)
	}
	if err := checkCoercion("null", configured.null); err != nil {
		return settings{}, err
	}
	if err := checkCoercion("undefined", configured.undefined); err != nil {
		return settings{}, err
	}
	if configured.memoryLimit < 0 {
		return settings{}, fault.From(InvalidTemplate, fmt.Sprintf("memory limit must be positive, got %d", configured.memoryLimit))
	}
	return configured, nil
}

func checkCoercion(name string, coercion Coercion) error {
	if coercion != Loose && coercion != Strict {
		return fault.From(InvalidTemplate, fmt.Sprintf("%s coercion must be jaml.Loose or jaml.Strict, got %d", name, coercion))
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
