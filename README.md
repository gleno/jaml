# jaml

Jaml documents are structured documents whose values can hold JavaScript. jaml parses a document into an `Expression` once, then `Resolve` evaluates the part you ask for and decodes the result into your Go type with `encoding/json/v2`. Nothing runs until you resolve, and everything lives as long as the context you made it with.

```sh
go get github.com/gleno/jaml
```

Requires Go 1.27.3 or newer with its default JSONv2 implementation. Explicit `GOEXPERIMENT=nojsonv2` builds are unsupported. JAML uses native JSONv2 defaults for Go inputs and resolved results. Since v0.7.0, struct fields match case-sensitively, nil slices and maps encode as `[]` and `{}`, and `omitempty` keeps `false` and `0` (use `omitzero` to omit them). Duplicate names and mismatched fixed-array lengths are rejected. Custom JSON methods also apply to map keys; errors expose native `json.SemanticError` or `jsontext.SyntacticError` causes. See [Go’s JSONv2 documentation](https://pkg.go.dev/encoding/json/v2) for the full type mapping and field options.

## Simple use

`${ … }` as a whole value keeps its result's type. `${{ … }}` inside text writes its result into the text. Values arrive on the context.

```go
type Config struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
}

sandbox, err := jaml.MakeSecureSandbox(appCtx)
document, err := sandbox.MakeExpressionFromJaml(appCtx, `
port: ${ base + 80 }
address: "${{ host }}:${{ base + 80 }}"
`)

ctx := jaml.WithValues(requestCtx, map[string]any{"host": "example.com", "base": 8000})
cfg, err := jaml.Resolve[Config](ctx, document)
// cfg == Config{Port: 8080, Address: "example.com:8080"}
```

Make the sandbox and the document on a long-lived context. Resolve per request with that request's values.

## Advanced use

Scripts travel on the context too. Code that produces a function is called with the arguments of the `Resolve`. A field typed `*jaml.Expression` is left unevaluated, so you can resolve it later, with its own arguments. `At` selects a part of the document, and the limits bound a runaway job.

```go
type Plan struct {
	Team  string           `json:"team"`
	Seats *jaml.Expression `json:"seats"`
}

sandbox, err := jaml.MakeSecureSandbox(appCtx, jaml.WithMemoryLimit(256<<20))
document, err := sandbox.MakeExpressionFromJaml(appCtx, `
plans:
  pro:
    team: "${{ label(team) }}"
    seats: "${ (tier) => tier.base * multiplier }"
`)

ctx := jaml.WithValues(requestCtx, map[string]any{"team": "sales", "multiplier": 10})
ctx = jaml.WithScript(ctx, `function label(name) { return name.toUpperCase() + ' plan' }`)
ctx = jaml.WithTimeLimit(ctx, 50*time.Millisecond)
ctx = jaml.WithWallLimit(ctx, time.Second)

plan, err := jaml.Resolve[Plan](ctx, document.At("plans", "pro"))
// plan.Team == "SALES plan"; plan.Seats holds the function, unevaluated
seats, err := jaml.Resolve[int](ctx, plan.Seats, map[string]any{"base": 3})
// seats == 30
```

A secure sandbox runs every expression in one child process with a memory ceiling, and replays the other jobs if a document brings the child down. A trusted sandbox runs in your process, for documents you wrote.

## Public API

```go
// Sandboxes. Each lives until its ctx ends, and so does everything made in it.
func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)                          // child process, memory ceiling, frozen built-ins and values so no piece of code can affect another
func MakeSecureSandboxWithoutBuiltinsProtection(ctx context.Context, options ...SandboxOption) (*Sandbox, error) // same child process, ordinary built-ins; cheaper per Resolve
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)                         // runs in your process, unprotected; for documents you wrote

// Expressions. Parsed and compiled once; nothing is evaluated until Resolve.
func (s *Sandbox) MakeExpressionFromJaml(ctx context.Context, jaml string, options ...Option) (*Expression, error) // a whole Jaml document
func (s *Sandbox) MakeExpression(ctx context.Context, expression string, options ...Option) (*Expression, error)   // a single JavaScript expression
func (s *Sandbox) Health() Health                                                                                  // resident memory, address ceiling and restarts of the sandbox's child

func (e *Expression) At(path ...string) *Expression               // a view of one part, one key or list index per step; never fails, a missing path fails at Resolve
func (e *Expression) Copy(ctx context.Context) (*Expression, error) // a new handle that lives until its own ctx ends
func Resolve[T any](ctx context.Context, e *Expression, args ...any) (T, error) // evaluate, call any function with args, decode into T

// Per-call settings, carried on the ctx of a Resolve.
func WithValues(ctx context.Context, values map[string]any) context.Context   // names the code can read; bound as JSON data
func WithScript(ctx context.Context, source string) context.Context          // JavaScript whose top-level declarations the code can call; repeat to add more
func WithFixedClock(ctx context.Context, time time.Time) context.Context     // what Date.now() and new Date() return
func WithRandomSeed(ctx context.Context, seed uint64) context.Context        // seeds Math.random(), for reproducible results
func WithTimeLimit(ctx context.Context, limit time.Duration) context.Context // CPU time one Resolve may compute; over it fails with TimeLimit
func WithWallLimit(ctx context.Context, limit time.Duration) context.Context // wall time one Resolve may run; over it fails with WallLimit
func WithMeter(ctx context.Context, sink func(Usage)) context.Context        // receives what the Resolve used, once, before it returns
func WithConsole(ctx context.Context, sink func(Line)) context.Context       // receives each console.log and friends, with the path of the code that wrote it

// Expression options.
func WithNullCoercion(coercion Coercion) Option      // Loose (default) lets null decode as encoding/json/v2 does; Strict refuses it
func WithUndefinedCoercion(coercion Coercion) Option // Loose turns undefined into null; Strict (default) refuses it
func AsFunction() Option                             // MakeExpression only: refuse anything but a function literal
func WithSigil(sigil string) Option                  // MakeExpressionFromJaml only: what opens code; default "$"

// Sandbox options.
func WithMemoryLimit(bytes int64) SandboxOption           // ceiling on the child's live heap; secure sandboxes only
func WithMaxConcurrency(jobs int) SandboxOption           // how many jobs run at once
func WithBuildTimeout(timeout time.Duration) SandboxOption // wall bound on making an expression or compiling a script
func WithBuildTimeLimit(limit time.Duration) SandboxOption // CPU bound on the same
func WithScriptCacheLimit(bytes int64) SandboxOption      // compiled scripts kept, least recently used first out; default an eighth of the memory limit
func WithCallDepthLimit(calls int) SandboxOption          // how deep JavaScript calls may nest; default 4096, at most 32768
func WithAttributedDeaths(heard func(ctx context.Context, death AttributedDeath)) SandboxOption // hears each child death pinned on one job by its replay alone, with that job's context values, even after its caller gave up: such a job is replayed after those whose callers wait, for at most a minute from the start of its replay, is dropped once the death is attributed, and a replay that runs past its minute is heard as ReplayDropped

// Reported types.
type Thrown struct { Message string; Value any }                    // what code threw, reachable with errors.As
type Failure struct { Path string; Reason string }                  // every jaml error's location and reason, reachable with errors.As
type Line struct { Path []string; Level Level; Text string }        // one console line; Level is Log, Info, Warn, Error or Debug
type Usage struct { CPU, Wall time.Duration; Resident int64 }       // what a Resolve used; Resident is bytes, zero off Linux
type Health struct { Resident, AddressCeiling int64; Restarts int } // the sandbox's child; Restarts counts children after the first
type AttributedDeath struct { Cause error }                         // the death a job's replay alone caused, matched like Resolve's error

// Errors. All derive from ExpressionError; each wraps its cause, so a cancelled call matches context.Canceled.
var (
	ExpressionError   // base sentinel for every jaml error
	InvalidExpression // bad document, bad JavaScript syntax, bad script or bad options
	InvalidTarget     // T holds a type encoding/json/v2 cannot decode into, or a jaml.Expression by value
	MissingPath       // the path names nothing in the document
	EvaluationFailed  // code threw or produced something that is not data; a call was cancelled; the child died
	CoercionFailed    // json.Unmarshal refused the data, or strict coercion refused null or undefined
	Closed            // the expression's context or its sandbox's has ended
	Unsupported       // a secure sandbox on a platform that cannot run one
	Unavailable       // a secure sandbox's child could not be started; worth retrying
	InvalidInput      // Resolve refused a value, argument or limit before running anything
	MemoryLimit       // an EvaluationFailed: the job ran out of memory
	TimeLimit         // an EvaluationFailed: the job computed longer than WithTimeLimit or WithBuildTimeLimit allows
	WallLimit         // an EvaluationFailed: the job ran longer than WithWallLimit allows
	CallDepthLimit    // an EvaluationFailed: calls nested deeper than WithCallDepthLimit allows
	EvaluatorDied     // an EvaluationFailed: the child crashed under this job
	StuckAfterCancel  // an EvaluationFailed: the child ended with a job stuck inside a built-in after it was cancelled
	ExitNotCollected  // an EvaluationFailed: the child ended and its exit could not be collected
	KilledFromOutside // an Unavailable: something outside jaml killed the child, often the kernel's out-of-memory killer
	ReplayDropped     // an Unavailable, heard by WithAttributedDeaths: a replay that would attribute a death ran past its bound
)
```

Every message leads with its location, `$` for the root and `$.plans.pro.seats` deeper:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: expression error
```

## License

See [LICENSE](LICENSE).
