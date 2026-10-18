package jaml

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	evaluatorMarker      = "JAML_EVALUATOR"
	evaluatorMemoryLimit = "JAML_MEMORY_LIMIT"
	evaluatorNonce       = "JAML_NONCE"
	evaluatorParent      = "JAML_PARENT"
	evaluatorMagic       = "jaml-evaluator"
	protocolVersion      = 2
	exitMemoryLimit      = 86
	requestsFD           = 3
	responsesFD          = 4
)

type hello struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Nonce   string `json:"nonce"`
}

type blueprint struct {
	Written   string   `json:"written"`
	Null      Coercion `json:"null"`
	Undefined Coercion `json:"undefined"`
}

type rendering struct {
	Path []string    `json:"path"`
	Vars encodedVars `json:"vars"`
}

type rendered struct {
	Data    json.RawMessage `json:"data,omitempty"`
	Failure *failure        `json:"failure,omitempty"`
}

type failure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type failureKind struct {
	name     string
	sentinel error
}

var failureKinds = []failureKind{
	{"InvalidTemplate", InvalidTemplate},
	{"InvalidTarget", InvalidTarget},
	{"MissingPath", MissingPath},
	{"EvaluationFailed", EvaluationFailed},
	{"CoercionFailed", CoercionFailed},
	{"Closed", Closed},
	{"Unsupported", Unsupported},
}

func (b blueprint) build() (*template, error) {
	configured, err := configure([]Option{WithNullCoercion(b.Null), WithUndefinedCoercion(b.Undefined)})
	if err != nil {
		return nil, err
	}
	return makeTemplate(b.Written, configured)
}

func answer(data []byte, err error) rendered {
	if err != nil {
		described := describeFailure(err)
		return rendered{Failure: &described}
	}
	return rendered{Data: data}
}

func describeFailure(err error) failure {
	failed, isTemplateError := err.(templateError)
	if !isTemplateError {
		failed = templateError{kind: EvaluationFailed, cause: err}
	}
	for _, kind := range failureKinds {
		if kind.sentinel == failed.kind {
			return failure{Kind: kind.name, Message: failed.cause.Error()}
		}
	}
	return failure{Kind: "EvaluationFailed", Message: failed.Error()}
}

func (r rendered) malformed() error {
	switch {
	case r.Failure != nil && len(r.Data) != 0:
		return errors.New("the evaluator answered with both data and a failure")
	case r.Failure != nil && r.Failure.kind() == nil:
		return fmt.Errorf("the evaluator answered with an unknown failure kind %q", r.Failure.Kind)
	case r.Failure == nil && len(r.Data) == 0:
		return errors.New("the evaluator answered with neither data nor a failure")
	}
	return nil
}

func (f failure) kind() error {
	for _, kind := range failureKinds {
		if kind.name == f.Kind {
			return kind.sentinel
		}
	}
	return nil
}

func (f failure) rebuild() error {
	kind := f.kind()
	if kind == nil {
		kind = EvaluationFailed
	}
	return templateError{kind: kind, cause: errors.New(f.Message)}
}
