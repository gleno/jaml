package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	evaluatorMarker      = "JAML_EVALUATOR"
	evaluatorMemoryLimit = "JAML_MEMORY_LIMIT"
	evaluatorBuiltins    = "JAML_BUILTINS"
	evaluatorCallDepth   = "JAML_CALL_DEPTH"
	evaluatorScripts     = "JAML_SCRIPT_CACHE"
	evaluatorBuildTime   = "JAML_BUILD_TIMEOUT"
	evaluatorBuildLimit  = "JAML_BUILD_TIME_LIMIT"
	evaluatorJobs        = "JAML_MAX_CONCURRENCY"
	mallocArenas         = "MALLOC_ARENA_MAX"
	evaluatorNonce       = "JAML_NONCE"
	evaluatorParent      = "JAML_PARENT"
	evaluatorMagic       = "jaml-evaluator"
	protocolVersion      = 23
	exitMemoryLimit      = 86
	exitStuckAfterCancel = 89
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
	Path      []string     `json:"path,omitempty"`
	Values    _BoundValues `json:"values,omitempty"`
	Scripts   []string     `json:"scripts,omitempty"`
	Args      []_Datum     `json:"args,omitempty"`
	Skips     *_Shape      `json:"skips,omitempty"`
	Pinned    _Pinned      `json:"pinned"`
	WallLimit _WallLimit   `json:"wall,omitempty"`
	TimeLimit _TimeLimit   `json:"limit,omitempty"`
	Console   bool         `json:"console,omitempty"`
}

type _Reply struct {
	Job     uint64        `json:"job"`
	Built   bool          `json:"built,omitempty"`
	Code    *_CodeOutline `json:"code,omitempty"`
	Data    _Borrowed     `json:"data,omitempty"`
	Skipped [][]string    `json:"skipped,omitempty"`
	Failure *_Failure     `json:"failure,omitempty"`
	Missing bool          `json:"missing,omitempty"`
	Line    *_LineOnWire  `json:"line,omitempty"`
	Usage   *_UsageOnWire `json:"usage,omitempty"`
}

type _UsageOnWire struct {
	CPU  int64 `json:"cpu"`
	Wall int64 `json:"wall"`
}

func (u *_UsageOnWire) reported() *Usage {
	if u == nil {
		return nil
	}
	var measured = u.measured()
	return &measured
}

func (u *_UsageOnWire) measured() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{CPU: time.Duration(u.CPU), Wall: time.Duration(u.Wall)}
}

type _LineOnWire struct {
	Path  []string `json:"path,omitempty"`
	Level Level    `json:"level"`
	Text  string   `json:"text"`
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

func (f _Frames) replies(source io.Reader, quiet *_Quiet) *_FrameReader {
	return &_FrameReader{source: source, limit: f.limit, refusal: fmt.Errorf("the evaluator sent a reply larger than %d bytes", f.limit), quiet: quiet}
}

func (f _Frames) requests(source io.Reader) *_FrameReader {
	var limit = f.limit + requestEnvelope
	return &_FrameReader{source: source, limit: limit, refusal: fmt.Errorf("a request larger than %d bytes", limit), quiet: &_Quiet{}}
}

type _Failure struct {
	Kind    string         `json:"kind"`
	At      string         `json:"at,omitempty"`
	Label   string         `json:"label,omitempty"`
	Cause   string         `json:"cause,omitempty"`
	Message string         `json:"message"`
	Thrown  *_ThrownOnWire `json:"thrown,omitempty"`
}

type _ThrownOnWire struct {
	Message string    `json:"message"`
	Value   _Borrowed `json:"value"`
}

func (b _Blueprint) build(ctx context.Context) (built _Servable, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = failWith(invalidExpression, failure)
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

func describeFailure(failed _ExpressionError) _Failure {
	var unplaced = failed.unplaced
	return _Failure{Kind: kinds[unplaced.kind].name, At: failed.path, Label: unplaced.label, Cause: causeOf(unplaced.cause), Message: unplaced.cause.Error()}
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
	case r.Code != nil && !r.Built:
		return errors.New("the evaluator outlined code for something other than a build")
	case r.Code != nil && r.Code.checked() != nil:
		return fmt.Errorf("the evaluator outlined code badly: %w", r.Code.checked())
	case r.Failure != nil && !r.Failure.known():
		return fmt.Errorf("the evaluator answered with an unknown failure kind %q", r.Failure.Kind)
	case r.Failure != nil && !r.Failure.knownCause():
		return fmt.Errorf("the evaluator answered with an unknown failure cause %q", r.Failure.Cause)
	case r.Failure != nil && r.Failure.At == "":
		return errors.New("the evaluator answered with a failure at no place")
	case r.Failure != nil:
		return nil
	case op == opBuild && !r.Built:
		return errors.New("the evaluator answered a build with something other than built")
	case op == opServe && r.Built:
		return errors.New("the evaluator answered an evaluation with a build")
	}
	return nil
}

func (f _Failure) known() bool {
	_, known := kindNamed(f.Kind)
	return known
}

func (f _Failure) knownCause() bool {
	_, known := causeNamed(f.Cause)
	return f.Cause == "" || known
}

func (f _Failure) rebuild() _ExpressionError {
	var kind, _ = kindNamed(f.Kind)
	var matched, _ = causeNamed(f.Cause)
	return _Unplaced{kind: kind, label: f.Label, cause: _CarriedCause{message: f.Message, thrown: f.Thrown.rebuild(), matched: matched}}.at(f.At)
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
	matched error
}

func (c _CarriedCause) Is(target error) bool {
	return c.matched != nil && target == c.matched
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
