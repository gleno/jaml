package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	evaluatorMarker      = "JAML_EVALUATOR"
	evaluatorMemoryLimit = "JAML_MEMORY_LIMIT"
	evaluatorNonce       = "JAML_NONCE"
	evaluatorParent      = "JAML_PARENT"
	evaluatorMagic       = "jaml-evaluator"
	protocolVersion      = 8
	exitMemoryLimit      = 86
	requestsFD           = 3
	responsesFD          = 4
	controlFD            = 5
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

type blueprint struct {
	Written   string   `json:"written"`
	Null      Coercion `json:"null"`
	Undefined Coercion `json:"undefined"`
	Raw       bool     `json:"raw,omitempty"`
	Function  bool     `json:"function,omitempty"`
	Sigil     string   `json:"sigil,omitempty"`
}

type work struct {
	Path    []string    `json:"path,omitempty"`
	Values  boundValues `json:"values,omitempty"`
	Scripts []string    `json:"scripts,omitempty"`
	Args    []datum     `json:"args,omitempty"`
	Skips   *_Shape     `json:"skips,omitempty"`
	Pinned  pinned      `json:"pinned"`
}

type reply struct {
	Job     uint64          `json:"job"`
	Built   bool            `json:"built,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Skipped [][]string      `json:"skipped,omitempty"`
	Failure *failure        `json:"failure,omitempty"`
	Missing bool            `json:"missing,omitempty"`
}

type frames struct {
	limit int
}

func framesFor(limit memoryLimit) frames {
	return frames{limit: int(min(int64(limit)/4, math.MaxInt))}
}

func wireJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

func (f frames) oversized() error {
	return fmt.Errorf("the evaluator sent a reply larger than %d bytes", f.limit)
}

type failure struct {
	Kind    string        `json:"kind"`
	At      string        `json:"at,omitempty"`
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
	{"InvalidExpression", InvalidExpression},
	{"InvalidTarget", InvalidTarget},
	{"MissingPath", MissingPath},
	{"EvaluationFailed", EvaluationFailed},
	{"CoercionFailed", CoercionFailed},
	{"Closed", Closed},
	{"Unsupported", Unsupported},
}

func (b blueprint) build(ctx context.Context) (built servable, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = fail(InvalidExpression, "%w", failure)
		}
	}()
	configured, err := configure(b.options())
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, cancelled(ctx)
	}
	if b.Raw {
		return compileRaw(b.Written, configured)
	}
	return makeDocument(ctx, b.Written, configured)
}

func (b blueprint) options() []Option {
	var options = []Option{WithNullCoercion(b.Null), WithUndefinedCoercion(b.Undefined)}
	if b.Function {
		options = append(options, AsFunction())
	}
	if b.Sigil != "" {
		options = append(options, WithSigil(b.Sigil))
	}
	return options
}

func buildPanic(recovered any) error {
	failure := engineFailure(recovered)
	if failure == nil {
		return nil
	}
	return errors.New(excerpt(failure.Error(), thrownExcerptLength))
}

func describeFailure(err error) failure {
	failed, isOwn := err.(expressionError)
	if !isOwn {
		failed = expressionError{kind: EvaluationFailed, cause: err}
	}
	described := failure{Kind: "EvaluationFailed", At: failed.at, Message: failed.cause.Error()}
	for _, kind := range failureKinds {
		if kind.sentinel == failed.kind {
			described.Kind = kind.name
		}
	}
	return described
}

func (r reply) outcomes() int {
	count := 0
	for _, present := range [...]bool{r.Built, len(r.Data) != 0, r.Failure != nil, r.Missing} {
		if present {
			count++
		}
	}
	return count
}

func (r reply) fits(op opcode) error {
	switch {
	case r.outcomes() > 1:
		return errors.New("the evaluator answered with more than one outcome")
	case r.outcomes() == 0:
		return errors.New("the evaluator answered with neither data nor a failure")
	case r.Failure != nil && r.Failure.kind() == nil:
		return fmt.Errorf("the evaluator answered with an unknown failure kind %q", r.Failure.Kind)
	case r.Failure != nil:
		return nil
	case op == opBuild && !r.Built:
		return errors.New("the evaluator answered a build with something other than built")
	case op == opServe && r.Built:
		return errors.New("the evaluator answered an evaluation with a build")
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
	return expressionError{kind: kind, at: f.At, cause: carriedCause{message: f.Message, thrown: f.Thrown.rebuild()}}
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
