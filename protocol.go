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

type _Hello struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Nonce   string `json:"nonce"`
}

type _Opcode string

const (
	opBuild  _Opcode = "build"
	opServe  _Opcode = "serve"
	opCancel _Opcode = "cancel"
	opForget _Opcode = "forget"
)

type _Request struct {
	Op   _Opcode     `json:"op"`
	Job  uint64      `json:"job,omitempty"`
	Unit uint64      `json:"unit,omitempty"`
	Plan *_Blueprint `json:"plan,omitempty"`
	Work *_Work      `json:"work,omitempty"`
}

type _Blueprint struct {
	Written   string   `json:"written"`
	Null      Coercion `json:"null"`
	Undefined Coercion `json:"undefined"`
	Raw       bool     `json:"raw,omitempty"`
	Function  bool     `json:"function,omitempty"`
	Sigil     string   `json:"sigil,omitempty"`
}

type _Work struct {
	Path    []string     `json:"path,omitempty"`
	Values  _BoundValues `json:"values,omitempty"`
	Scripts []string     `json:"scripts,omitempty"`
	Args    []_Datum     `json:"args,omitempty"`
	Skips   *_Shape      `json:"skips,omitempty"`
	Pinned  _Pinned      `json:"pinned"`
}

type _Reply struct {
	Job     uint64          `json:"job"`
	Built   bool            `json:"built,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Skipped [][]string      `json:"skipped,omitempty"`
	Failure *_Failure       `json:"failure,omitempty"`
	Missing bool            `json:"missing,omitempty"`
}

type _Frames struct {
	limit int
}

func framesFor(limit _MemoryLimit) _Frames {
	return _Frames{limit: int(min(int64(limit)/4, math.MaxInt))}
}

func wireJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	var encoder = json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

func (f _Frames) oversized() error {
	return fmt.Errorf("the evaluator sent a reply larger than %d bytes", f.limit)
}

type _Failure struct {
	Kind    string         `json:"kind"`
	At      string         `json:"at,omitempty"`
	Message string         `json:"message"`
	Thrown  *_ThrownOnWire `json:"thrown,omitempty"`
}

type _ThrownOnWire struct {
	Message string          `json:"message"`
	Value   json.RawMessage `json:"value"`
}

type _FailureKind struct {
	name     string
	sentinel error
}

var failureKinds = []_FailureKind{
	{"InvalidExpression", InvalidExpression},
	{"InvalidTarget", InvalidTarget},
	{"MissingPath", MissingPath},
	{"EvaluationFailed", EvaluationFailed},
	{"CoercionFailed", CoercionFailed},
	{"Closed", Closed},
	{"Unsupported", Unsupported},
}

func (b _Blueprint) build(ctx context.Context) (built _Servable, err error) {
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

func (b _Blueprint) options() []Option {
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
	var failure = engineFailure(recovered)
	if failure == nil {
		return nil
	}
	return errors.New(excerpt(failure.Error(), thrownExcerptLength))
}

func describeFailure(err error) _Failure {
	failed, isOwn := err.(_ExpressionError)
	if !isOwn {
		failed = _ExpressionError{kind: EvaluationFailed, cause: err}
	}
	var described = _Failure{Kind: "EvaluationFailed", At: failed.path, Message: failed.cause.Error()}
	for _, kind := range failureKinds {
		if kind.sentinel == failed.kind {
			described.Kind = kind.name
		}
	}
	return described
}

func (r _Reply) outcomes() int {
	var count = 0
	for _, present := range [...]bool{r.Built, len(r.Data) != 0, r.Failure != nil, r.Missing} {
		if present {
			count++
		}
	}
	return count
}

func (r _Reply) fits(op _Opcode) error {
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

func (f _Failure) kind() error {
	for _, kind := range failureKinds {
		if kind.name == f.Kind {
			return kind.sentinel
		}
	}
	return nil
}

func (f _Failure) rebuild() error {
	var kind = f.kind()
	if kind == nil {
		kind = EvaluationFailed
	}
	return _ExpressionError{kind: kind, path: f.At, cause: _CarriedCause{message: f.Message, thrown: f.Thrown.rebuild()}}
}

func (t *_ThrownOnWire) rebuild() *Thrown {
	if t == nil {
		return nil
	}
	return newThrown(t.Message, t.Value)
}

type _CarriedCause struct {
	message string
	thrown  *Thrown
}

func (c _CarriedCause) Error() string {
	return c.message
}

func (c _CarriedCause) Unwrap() error {
	if c.thrown == nil {
		return nil
	}
	return c.thrown
}
