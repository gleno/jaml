package jaml

import (
	"fmt"
	"reflect"
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
)

type Coercion int

const (
	Loose Coercion = iota + 1
	Strict
)

const defaultTimeout = time.Second

type Option func(*settings)

type settings struct {
	null      Coercion
	undefined Coercion
	timeout   time.Duration
}

func WithNullCoercion(coercion Coercion) Option {
	return func(s *settings) { s.null = coercion }
}

func WithUndefinedCoercion(coercion Coercion) Option {
	return func(s *settings) { s.undefined = coercion }
}

func WithTimeout(timeout time.Duration) Option {
	return func(s *settings) { s.timeout = timeout }
}

type Template struct {
	root     value
	rootAt   position
	settings settings
}

func MakeTemplate(text string, options ...Option) (*Template, error) {
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	root, rootAt, err := parseTemplate(text)
	if err != nil {
		return nil, err
	}
	return &Template{root: root, rootAt: rootAt, settings: configured}, nil
}

func (t *Template) Eval(into any, vars map[string]any, path ...string) error {
	target := reflect.ValueOf(into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fault.From(InvalidTarget, fmt.Sprintf("Eval needs a non-nil pointer, got %s", describeTarget(into)))
	}
	selected, at, err := selectPath(t.root, rootLocation(t.rootAt), path)
	if err != nil {
		return err
	}
	evaluation := openEvaluation(t.settings, vars)
	defer evaluation.close()
	return evaluation.decode(selected, destination{target: target.Elem(), at: at})
}

func configure(options []Option) (settings, error) {
	configured := settings{null: Loose, undefined: Strict, timeout: defaultTimeout}
	for _, option := range options {
		option(&configured)
	}
	for name, coercion := range map[string]Coercion{"null": configured.null, "undefined": configured.undefined} {
		if coercion != Loose && coercion != Strict {
			return settings{}, fault.From(InvalidTemplate, fmt.Sprintf("%s coercion must be jaml.Loose or jaml.Strict, got %d", name, coercion))
		}
	}
	if configured.timeout <= 0 {
		return settings{}, fault.From(InvalidTemplate, fmt.Sprintf("timeout must be positive, got %s", configured.timeout))
	}
	return configured, nil
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
