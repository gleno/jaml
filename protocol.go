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
	protocolVersion      = 4
	exitMemoryLimit      = 86
	requestsFD           = 3
	responsesFD          = 4
)

type hello struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Nonce   string `json:"nonce"`
}

type opcode string

const (
	opBuild  opcode = "build"
	opServe  opcode = "serve"
	opCancel opcode = "cancel"
	opForget opcode = "forget"
)

type request struct {
	Op   opcode     `json:"op"`
	Job  uint64     `json:"job,omitempty"`
	Unit uint64     `json:"unit,omitempty"`
	Plan *blueprint `json:"plan,omitempty"`
	Work *work      `json:"work,omitempty"`
}

func (r request) withJob(job uint64) request {
	r.Job = job
	return r
}

type blueprint struct {
	Written    string   `json:"written"`
	Null       Coercion `json:"null"`
	Undefined  Coercion `json:"undefined"`
	Expression bool     `json:"expression,omitempty"`
}

type work struct {
	Path   []string  `json:"path,omitempty"`
	Vars   boundVars `json:"vars,omitempty"`
	Call   *callArgs `json:"call,omitempty"`
	Pinned pinned    `json:"pinned"`
}

type callArgs struct {
	Args []datum `json:"args"`
}

type reply struct {
	Job     uint64          `json:"job"`
	At      *string         `json:"at,omitempty"`
	Built   bool            `json:"built,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Failure *failure        `json:"failure,omitempty"`
}

type failure struct {
	Kind    string        `json:"kind"`
	Message string        `json:"message"`
	Thrown  *thrownOnWire `json:"thrown,omitempty"`
}

type thrownOnWire struct {
	Message string          `json:"message"`
	Value   json.RawMessage `json:"value"`
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

func (b blueprint) build() (built servable, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = fail(InvalidTemplate, "%s: %w", (*dataPath)(nil), failure)
		}
	}()
	configured, err := configure([]Option{WithNullCoercion(b.Null), WithUndefinedCoercion(b.Undefined)})
	if err != nil {
		return nil, err
	}
	if b.Expression {
		return compileStandalone(b.Written, configured)
	}
	return makeTemplate(b.Written, configured)
}

func buildPanic(recovered any) error {
	failure := engineFailure(recovered)
	if failure == nil {
		return nil
	}
	return errors.New(excerpt(failure.Error(), thrownExcerptLength))
}

func describeFailure(err error) failure {
	failed, isTemplateError := err.(templateError)
	if !isTemplateError {
		failed = templateError{kind: EvaluationFailed, cause: err}
	}
	described := failure{Kind: "EvaluationFailed", Message: failed.Error(), Thrown: thrownOnWireFrom(err)}
	for _, kind := range failureKinds {
		if kind.sentinel == failed.kind {
			described.Kind, described.Message = kind.name, failed.cause.Error()
		}
	}
	return described
}

func thrownOnWireFrom(err error) *thrownOnWire {
	var caught *Thrown
	if !errors.As(err, &caught) {
		return nil
	}
	value, marshalErr := json.Marshal(caught.Value)
	if marshalErr != nil {
		value = json.RawMessage("null")
	}
	return &thrownOnWire{Message: caught.Message, Value: value}
}

func (r reply) outcomes() int {
	count := 0
	for _, present := range [...]bool{r.At != nil, r.Built, len(r.Data) != 0, r.Failure != nil} {
		if present {
			count++
		}
	}
	return count
}

func (r reply) malformed() error {
	switch {
	case r.outcomes() > 1:
		return errors.New("the evaluator answered with more than one outcome")
	case r.outcomes() == 0:
		return errors.New("the evaluator answered with neither data nor a failure")
	case r.Failure != nil && r.Failure.kind() == nil:
		return fmt.Errorf("the evaluator answered with an unknown failure kind %q", r.Failure.Kind)
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
	return templateError{kind: kind, cause: carriedCause{message: f.Message, thrown: f.Thrown.rebuild()}}
}

func (t *thrownOnWire) rebuild() *Thrown {
	if t == nil {
		return nil
	}
	return newThrown(t.Message, t.Value)
}

type carriedCause struct {
	message string
	thrown  *Thrown
}

func (c carriedCause) Error() string {
	return c.message
}

func (c carriedCause) Unwrap() error {
	if c.thrown == nil {
		return nil
	}
	return c.thrown
}
