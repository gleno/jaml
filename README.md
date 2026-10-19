# jaml

YAML templates whose values can hold JavaScript expressions, evaluated against your variables when you decode. jaml parses the YAML into plain data once, walks it at `Eval` time replacing every expression with its result, then hands the data to `encoding/json` to decode into your type. Standalone expressions and functions work the same way.

## Install

```sh
go get github.com/gleno/jaml
```

## Lifetimes are contexts

Nothing in jaml has a `Close` method. **Everything lives exactly as long as the context you made it with.**

- A `Sandbox` lives until the context passed to `MakeSandbox` or `MakeTrustedSandbox` ends. That ends every template and expression in it, and an isolated sandbox's child process.
- A `Template` or `Expression` lives until the context passed to `MakeTemplate` or `Compile` ends, or its sandbox ends.
- After that, every call returns `Closed`.

So **pass a long-lived context** to the constructors: your service's root context, not a request's. A template made with a request-scoped context dies when the request ends. Each call (`Eval`, `Call`) takes its own context, which bounds only that call.

A template or expression you drop without ending its context is still forgotten: once the garbage collector finds it unreachable, jaml ends its lifetime and tells the sandbox to drop it.

## What it gives you

- **Two expression forms.** `${ … }` as a whole value is a typed result (a number stays a number, a list stays a list). `${{ … }}` inside text interpolates.
- **Nothing runs until the data arrives.** Making a template parses the YAML and compiles every expression, so syntax errors surface immediately, but evaluates nothing.
- **Results are data.** An expression must produce JSON data: scalars, lists, objects. Functions, symbols, BigInts, proxies, cycles, `NaN`, `Date`s, class instances, and strings or keys holding an unpaired surrogate are errors naming the path to the offending value.
- **Decoding is `encoding/json`.** Struct tags, embedded structs, `json.Unmarshaler`, `json.RawMessage`: whatever `json.Unmarshal` does, jaml does.
- **Null and undefined on your terms.** `Loose` or `Strict` for expression results, configured separately.
- **Expressions and functions.** `Compile` an expression on its own, then `Eval` it with vars or `Call` the function it evaluates to with arguments. The function never leaves the sandbox.
- **Deterministic when you want.** The clock and random seed travel on the context.
- **Thrown values as data.** What an expression threw is reachable as `*jaml.Thrown`, with its message and a JSON snapshot of the value.
- **Isolated or trusted, chosen up front.** An isolated sandbox runs everything in one child process with a memory ceiling, and survives anything a template does. A trusted sandbox runs in your process.
- **Safe to share.** Sandboxes, templates and expressions are safe for concurrent use from any number of goroutines.

## Usage

```go
import "github.com/gleno/jaml"

const text = `
config:
  value1: 123
  value2: ${1+1}
  value3: "some string ${{name}}"
`

type Config struct {
	Value1 int    `json:"value1"`
	Value2 int    `json:"value2"`
	Value3 string `json:"value3"`
}

sandbox, err := jaml.MakeSandbox(appCtx, jaml.WithMemoryLimit(512<<20))
if err != nil {
	return err
}
template, err := sandbox.MakeTemplate(appCtx, text)
if err != nil {
	return err
}

var cfg Config
err = template.Eval(requestCtx, &cfg, map[string]any{"name": "abc"}, "config")
// cfg == Config{Value1: 123, Value2: 2, Value3: "some string abc"}
```

`Eval(ctx, into, vars, path...)` decodes into the non-nil pointer `into`. The optional `path` selects a subtree first, one key or list index per element (`"config"`, `"servers", "0"`); only that subtree is evaluated. A list index is written in plain decimal, with no sign or leading zero. A path that does not exist is a `MissingPath` error.

`vars` must be JSON data. Every `Eval` checks and binds all of them before evaluating anything, whatever the path selects. A var that `encoding/json` cannot marshal, that nests deeper than 4096, that holds text or keys that are not valid UTF-8, or whose name is already a JavaScript global (`JSON`, `Math`, `globalThis`, `undefined`, `NaN`, …) is an `EvaluationFailed` error naming the var, never a template path.

### Expressions and functions

```go
check, err := sandbox.Compile(appCtx, "x => x.end >= x.start || 'end before start'")
if err != nil {
	return err
}
var verdict any
err = check.Call(ctx, &verdict, map[string]any{"start": 2, "end": 1})
// verdict == "end before start"

sum, err := sandbox.Compile(appCtx, "a + b")
var total int
err = sum.Eval(ctx, &total, map[string]any{"a": 1, "b": 2})
// total == 3
```

`Compile` takes one JavaScript expression, under the same rules as a whole-value expression in a template, without the `${ }`. `Eval` evaluates it with `vars` as globals and decodes the result. `Call` requires the expression to evaluate to a function, calls it with `args`, each passed as a JSON copy (checked like vars, named `argument 1`, `argument 2`, …), and decodes what it returns. Results follow the same coercion options as templates, with the same defaults: null is `Loose` and undefined is `Strict`, so a validator that returns `undefined` for "no error" wants `jaml.WithUndefinedCoercion(jaml.Loose)`. A function value never leaves the sandbox: an expression or call that produces one is an `EvaluationFailed` error. Errors locate an expression at `$`, its result: `$: x => x.y.z: TypeError: …`.

### Clock and seed

```go
ctx = jaml.WithFixedClock(ctx, at)
ctx = jaml.WithRandomSeed(ctx, seed)
```

Every `Eval` and `Call` pins a clock and a random seed when it is submitted: the ones on its context, or else the current time and a fresh random seed. `Date.now()` and `new Date()` return the pinned time for the whole call, and `Math.random()` draws from a generator seeded with the pinned seed. With both set, results are identical in both sandbox kinds and on every run. The clock is pinned to the millisecond, the way JavaScript sees it: any instant is accepted, its zone and anything below a millisecond are dropped, and an instant beyond about 292 million years from 1970 is held at that bound, the same in both kinds. Past the JavaScript `Date` range (±8.64e15 ms) `Date.now()` still returns the pinned number, and `new Date()` is an `Invalid Date`. Pinning at submission also means that a call replayed after a crash (see below) sees exactly what its first attempt saw.

### Thrown values

```go
var thrown *jaml.Thrown
if errors.As(err, &thrown) {
	_ = thrown.Message // "TypeError: no" or "threw an object", collapsed and cut at 200 characters
	_ = thrown.Value   // a JSON snapshot of the thrown value
}
```

An error from an expression that threw wraps a `*jaml.Thrown`, inside an `EvaluationFailed`. `Value` is what the thrown value looks like as JSON, read without calling `toJSON`: numbers are `float64`, lists `[]any`, objects `map[string]any`. Inside objects, functions, symbols and `undefined` are left out; inside lists they become `nil`. `NaN`, infinities and BigInts become `nil`. An `Error` or class instance contributes its own enumerable properties (`e.status = 404` gives `{"status": 404}`). Cycles, proxies and anything nested deeper than 4096 become `nil`. If reading the value throws, or it holds more than 1,048,576 values, `Value` is `nil`. Getters run while it is read, inside the sandbox. No JavaScript value ever reaches your code.

## Syntax

```yaml
port: ${base + 1}                    # whole value: typed result
url: "https://${{host}}:${{port}}/"  # interpolation: always text
limits: "${ {cpu: 2, memory: '1Gi'} }"
steps: |
  ${
    items
      .filter(item => item.enabled)
      .map(item => item.name)
  }
```

- A value is a **whole expression** when, after trimming whitespace, it starts with `${`, ends with `}`, and the expression ends at that final `}`. Quoting does not matter: `${1+1}`, `"${1+1}"` and `'${1+1}'` are all the number 2.
- A value that starts with `${` but is not one whole expression (`${a} and ${b}`) is an error. Interpolate with `${{ }}` instead.
- **Interpolation** with `${{ … }}` always produces text, even when it is the whole value: `${{1+1}}` is `"2"`. To write an object literal as a whole value, put a space between the braces: `${ {a: 1} }`. Strings interpolate as they are; numbers, bools, lists and objects interpolate as JSON, keys sorted, written as `JSON.stringify` writes them: `${{ ({b: '<&>', a: 1}) }}` is `{"a":1,"b":"<&>"}`.
- An expression ends at the first closing `}` (or `}}` for interpolation) that JavaScript itself does not consume, so braces inside strings, template literals, regex literals and comments never close it: `${ /}/.test(x) }` and `${{ {a: 1}}}` work. A `//` comment runs to the end of its line, closing brace included; close the expression on the next line.
- A single `${` anywhere other than the start of a value is plain text: `echo ${HOME}` stays as written.
- To write the delimiters literally, use a JavaScript string: `${{'${{'}}` or `${'${x}'}`.
- **Keys are never evaluated.**
- An expression is exactly one strict-mode JavaScript expression, evaluated as a top-level script expression: statements (`1; 2`), legacy octal escapes (`"\101"`) and `new.target` are syntax errors, `arguments` is a `ReferenceError`, and `this` is `globalThis`. A body holding only comments is an empty expression. The vars are globals, deep-copied from `vars` the way `encoding/json` marshals them, so an expression cannot change your data. They never replace a built-in: a var named like one is refused. A var name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing var is a `ReferenceError`; a missing property of a var is `undefined`.
- All expressions in one `Eval` share one runtime, so one expression can see globals another one set. Separate `Eval` calls never share anything.
- `Date` and `Math.random` are pinned per call: the clock stands still for the whole `Eval`, and the random sequence comes from one seed. See [Clock and seed](#clock-and-seed).

### YAML gotchas

`go.yaml.in/yaml/v3` parses the YAML; jaml resolves every value itself, by the rules below, into JSON data.

- **Quote expressions that contain `: `, `#`, or start a flow collection.** `limits: ${ {cpu: 2} }` is a YAML error, because `: ` inside a plain scalar starts a mapping. Write `limits: "${ {cpu: 2} }"`. Inside flow collections (`[…]`, `{…}`) every expression needs quotes. The same goes for `?:` ternaries.
- **Only `true` and `false` are bools**, in any of their YAML spellings (`True`, `FALSE`). `y`, `yes`, `on`, `n` stay text.
- **Keys must be strings.** `1: x` or `true: x` is an `InvalidTemplate` error naming the path; quote the key.
- **Duplicate keys are refused**, with the line of the first one. Keys are compared after they resolve, so `a` and `!!binary YQ==` are the same key.
- **`.inf` and `.nan` are refused**, since JSON cannot carry them, and so are numbers too large for a 64-bit float (`1e400`). Quote them to keep them as text.
- **Dates stay text.** `2026-10-18` is the string `"2026-10-18"`, as written. A `time.Time` target still parses an RFC 3339 date-time such as `2026-10-18T10:00:00Z`.
- **Numbers follow one grammar**, plain or tagged. Integers are decimal (`1000`), `0x1F`, `0o17` or `0b101`; floats are decimal with an optional fraction and exponent (`1.5`, `.5`, `1.`, `1e10`, `-2.5E-3`). Underscores may sit between two digits anywhere in a number, or between a base prefix and its first digit (`1_000`, `.5_0`, `1e1_0`, `0xdead_beef`, `0x_ff`), and are dropped. An underscore anywhere else in a spelling that is otherwise a number (`1_`, `1__0`, `0x__ff`, `1_.5`, `1e_3`) is an `InvalidTemplate` error naming the path, never silent text. Uppercase prefixes (`0X1F`, `0B101`, `0O17`) are text, as in YAML 1.2, and so is anything starting with an underscore (`_1`). `!!int` takes the integer spellings and `!!float` the decimal ones, so `!!float 0x10` is an error.
- **Integers beyond int64 are text.** `123456789012345678901234567890` is the string `"123456789012345678901234567890"`, digits intact. Integers that fit int64 are numbers.
- **No silent octal.** `0644` is 420 in YAML 1.1 and 644 in YAML 1.2, so jaml keeps it as the text `"0644"`, and the same goes for every integer with a leading zero, underscores included (`0_644`, `-0_755`). Write `0o644` for octal or `644` for decimal; `!!int 0644` is octal.
- Anchors, aliases and merge keys (`<<`) expand. A merge key takes a mapping or a list of mappings; the mapping's own keys win, then earlier mappings in the list win over later ones. Errors inside merged or aliased values name the path where the value lands, such as `$.x.a`. Expanded data counts toward the 4096 depth limit, and aliasing is bounded the way `go.yaml.in/yaml/v3` bounds it: once more than 100 aliased values and 1000 values in all have been read, aliased values may make up at most 99% of the values read, falling linearly from 400,000 values to 10% at 4,000,000. On top of that, aliases may expand to at most 1,048,576 values. Excessive aliasing is an `InvalidTemplate` error naming the path where it tipped over.
- Tags are applied and then forgotten: `!!str 12` is the text `"12"`, `!!float 3` the number 3. A tag jaml does not know keeps the value as text. `!!binary` is base64 that must decode to UTF-8 text, which becomes the value; other bytes are an `InvalidTemplate` error naming the path.
- **One document per template.** A second document (`---` followed by content) is an error.

## Decoding

After evaluation the selected subtree is plain JSON data, decoded with `json.Unmarshal`. A value that does not fit its target, such as text into an `int` field, is a `CoercionFailed` error that leads with the path of the value that did not fit (`$.servers[1].port`) and wraps `encoding/json`'s error, so `errors.As` reaches a `*json.UnmarshalTypeError` or the error your own `UnmarshalJSON` returned. There is no extra coercion: a number into a `string` field is an error, so interpolate (`"${{port}}"`) when you want text. Numbers into `any` are `float64`.

## Null and undefined

YAML `~`, `null` and empty values are null and decode as JSON null. The settings apply to what expressions produce:

| | `Loose` | `Strict` |
| --- | --- | --- |
| null | JSON null | an error |
| undefined | JSON null | an error |

**Defaults:** null is `Loose`, undefined is `Strict` (it usually means a typo).

- `${{ }}` of null or undefined interpolates `""` when loose, and fails when strict.
- Inside a returned list, `undefined` becomes `null`; inside a returned object, a key holding `undefined` is dropped, as `JSON.stringify` does.
- JSON null leaves a non-pointer field as `encoding/json` leaves it: unchanged.

## Sandboxes

goja, the JavaScript engine, cannot limit memory, and it cannot be interrupted inside a built-in call such as a backtracking regular expression or `'x'.repeat(1e9)`. So jaml offers two kinds of sandbox with the same API, and templates and expressions behave identically in both apart from the isolation guarantees.

```go
func MakeSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)

func WithMemoryLimit(bytes int64) SandboxOption        // isolated only; 16 MiB to 1 TiB; default 256 MiB
func WithMaxConcurrency(jobs int) SandboxOption        // at least 1; default GOMAXPROCS
func WithBuildTimeout(timeout time.Duration) SandboxOption // positive; default 5s
```

Both kinds run at most `WithMaxConcurrency` jobs at once, in arrival order; a job is building a template, compiling an expression, an `Eval` or a `Call`. `WithBuildTimeout` bounds building and compiling themselves, not the time a job waits for its slot; the context bounds both. A build whose context or timeout ends stops between YAML nodes and expressions, and the call returns at once with the cause. A bad option is an `InvalidTemplate` error.

### Trusted

`MakeTrustedSandbox` runs everything in your process. The context of each call interrupts its JavaScript cooperatively, so an infinite loop ends with its context, but a built-in that never returns to the interpreter runs on, and an allocation bomb takes your process down. A build that outlives its context or timeout returns to you at once, and keeps its concurrency slot until it has stopped. `WithMemoryLimit` is refused. Use it for templates you wrote. Untrusted *vars* are fine: they are bound as JSON data and never run. An expression that hands an untrusted string to `eval`, `new Function` or `new RegExp` runs it as code; trusted mode makes no promise about running untrusted code.

### Isolated

`MakeSandbox` starts one child process that jaml owns and runs every template and expression of the sandbox in it, many at once, each job in its own fresh JavaScript runtime.

- **Building happens in the child.** Your process never interprets template or expression text: it hands the text to the child, which parses and compiles it. A refusal comes back with the same kind and message a trusted sandbox gives. Your process keeps each live template's text, to build it again in a fresh child when it is next used.
- **Memory.** One ceiling: the child's live heap may grow at most `WithMemoryLimit` over what it held when it started, so heap that your packages build in `init()` does not count. Built templates and expressions are resident and count. While any job runs, the child checks its heap every millisecond; past the ceiling it collects garbage once, and if the live heap is still over, it exits and the job that caused it fails with `MemoryLimit` ("exceeded the memory limit of N bytes"), which is an `EvaluationFailed`. A doubling loop can reach about twice the limit before it is stopped. A child whose allocation the operating system refuses outright is a `MemoryLimit` too. A child killed from outside jaml, usually by the kernel's out-of-memory killer, is a `MemoryLimit` when its live heap had passed three quarters of the ceiling; otherwise it fails with "evaluator died (signal: killed): killed from outside jaml", naming its peak resident memory on Linux.
- **Time.** When a call's context ends, the call returns at once. It fails with `EvaluationFailed` matching the context's cause (`context.DeadlineExceeded`, `context.Canceled` or your `context.Cause`) and names the selected path as you asked for it, whether the call was running or still waiting for its slot. The child interrupts the job, and the job keeps its concurrency slot until the child has let go of it. A job stuck inside a built-in gets 250 ms from the moment the child is told; then the child is killed. A build whose context ends is stopped by the child at its next YAML node or expression, never by a kill. Only `WithBuildTimeout` kills for a build: when one parse or compile step outlives it by 250 ms, the child is killed, and its other jobs are replayed as below. A call whose context is already done fails at once, without touching the child.
- **Recovery.** When the child dies for any reason, every job it was running is a suspect. jaml replays the suspects one at a time, each in a fresh child of its own that holds nothing but that job's template, with its pinned clock and seed, while new jobs wait. A suspect that kills the child while running alone fails with its real cause: `MemoryLimit`, or "evaluator died" with its exit status and the reason line of its crash, never its goroutine dump. Every other suspect returns its normal result, as if nothing had happened. Then the sandbox runs jobs concurrently again, in another fresh child. A child that cannot be started fails every job waiting for it, once, with the start error; the next call tries again. A job whose own deadline forced the kill is not replayed; it fails with its context's cause. jaml does not judge: it never flags or refuses a template, and a template whose `Eval` ran out of memory once is tried again on its next `Eval`. Jobs that only overflowed together all succeed on replay. Replay is safe because jobs are pure: vars and arguments are JSON copies, and there is no I/O and no callback into your process.
- **Replies.** Each answer travels back to your process as one message of at most a quarter of `WithMemoryLimit` (64 MiB by default), whatever it holds: data, a thrown value or a failure. A bigger one is refused in the child with `EvaluationFailed` ("$.path: the answer is larger than N bytes"); control characters count as the six bytes they take escaped. So your process never holds more than that per running job. A trusted sandbox has no such cap. A child that sends a bigger message anyway is killed, and its jobs see that as its death.
- **Clean environment.** The child gets none of your environment variables. It gets exactly `JAML_EVALUATOR`, `JAML_MEMORY_LIMIT`, `JAML_NONCE` (per start), `JAML_PARENT` (your process id), `ZONEINFO` when you have it set, and `TZ` when your local zone can be forwarded. A package whose `init()` needs another variable fails in the child.
- **Local time.** The child uses your local zone when it can find it the way you did: a zone from `TZ` at startup, by name or by the path of its file, or one you set with `time.Local, _ = time.LoadLocation(name)`, including one found through `ZONEINFO`. Then trusted and isolated sandboxes agree on local `Date` methods. A zone without such a name (`time.FixedZone`, a renamed or hand-built location) cannot be forwarded, so the child uses the system zone. The zone is read when a child starts.
- **Signals and parents.** The child runs in its own process group, so a terminal Ctrl-C or a signal sent to your process group does not reach it. It exits when your process dies: at once when idle, within about a millisecond when busy. An idle child uses no CPU.
- **Cost.** Starting a child takes about 5 ms and is bounded by `WithBuildTimeout` and the sandbox's context, never by the call that needed it. Recovery costs one start per suspect. An `Eval` is one request to the child, which sends back the data, decoded into your target in your process.

The child is your own binary, started again with a marker in its environment; on Linux it is the running image (`/proc/self/exe`), even if the file has been replaced since. jaml's `init()` sees the marker, identifies itself to the parent with a greeting that carries the protocol version and the nonce, serves over file descriptors 3, 4 and 5, and exits, so your `main()` never runs in the child. The `init()` functions of packages that Go initializes before jaml do run there. A child that answers with something other than its greeting, because the executable is not a jaml binary or speaks another protocol version, is `Unsupported`, with a message naming which. A child that dies before it greets, such as one whose `init()` panics, fails with `EvaluationFailed`, naming its exit status and the reason line of its crash. Isolated sandboxes need a Unix that can start the binary again: they are not supported on Windows, iOS, js or wasip1 (`Unsupported`), or when jaml is built into a C shared library or a plugin.

### Limits

Both kinds share these limits:

- JavaScript calls nest at most 4096 deep; deeper recursion fails with `EvaluationFailed`.
- A value that holds code (a whole expression, or text with `${{`) is at most 65,536 bytes as written; a longer one is refused with `InvalidTemplate`, naming its path. This bounds how deep goja's recursive parser can go, so no template text can overflow the stack while it is compiled.
- Vars, results and templates nest at most 4096 deep. Deeper data is refused, naming the var or the path where it crosses the limit: a template with `InvalidTemplate`, a var or a result with `EvaluationFailed`.
- `JSON.parse` refuses text nested deeper than 4096 with a `RangeError`, so a deep string var cannot be turned into deep data.
- Data an expression builds for itself is not bounded. Some built-ins (`JSON.stringify`, `String` or `join` of an array, `flat`) walk it recursively on the Go stack, and a value nested about a million deep overflows that stack, which Go cannot recover from. It is the template's own risk, like an allocation bomb: a trusted sandbox takes your process down; an isolated sandbox's child dies, and that `Eval` fails with the cause once it is replayed alone.
- A Go panic inside the engine, such as goja's `'x'.repeat(2 ** 52)`, fails the `Eval` with `EvaluationFailed` instead of reaching your code.
- A Go panic while building, such as goja's parser on `'\u{10FFFF}'`, refuses the template or expression with `InvalidTemplate`, naming the value's path (`$` for an expression); neither the host nor an isolated child goes down.
- A result may hold at most 1,048,576 values, each list and object counting as one. A bigger result is refused as a whole (`result holds more than 1048576 values`), before jaml allocates room for it, so a sparse list cannot claim more than it holds.
- The runtime has no I/O, no `require`, no timers, and source-map comments are not followed.

## API

```go
func MakeSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func (s *Sandbox) MakeTemplate(ctx context.Context, text string, options ...Option) (*Template, error)
func (s *Sandbox) Compile(ctx context.Context, source string, options ...Option) (*Expression, error)

func (t *Template) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error
func (e *Expression) Eval(ctx context.Context, into any, vars map[string]any) error
func (e *Expression) Call(ctx context.Context, into any, args ...any) error

func WithFixedClock(ctx context.Context, at time.Time) context.Context
func WithRandomSeed(ctx context.Context, seed uint64) context.Context

func WithNullCoercion(coercion Coercion) Option      // Loose (default) or Strict
func WithUndefinedCoercion(coercion Coercion) Option // Loose or Strict (default)
func WithMemoryLimit(bytes int64) SandboxOption
func WithMaxConcurrency(jobs int) SandboxOption
func WithBuildTimeout(timeout time.Duration) SandboxOption

type Thrown struct {
	Message string
	Value   any
}
```

### Errors

```go
var (
	TemplateError    // base sentinel for every jaml error
	InvalidTemplate  // bad YAML, bad JavaScript syntax, bad options
	InvalidTarget    // into is not a non-nil pointer
	MissingPath      // the path names nothing in the template
	EvaluationFailed // an expression threw or produced something that is not data; a call was cancelled; an isolated child ran out of memory or died
	CoercionFailed   // json.Unmarshal refused the data, or strict null/undefined refused a result
	Closed           // the template's, expression's or sandbox's context has ended
	Unsupported      // an isolated sandbox on a platform that cannot run one, or a child that is not a jaml evaluator
	MemoryLimit      // derives from EvaluationFailed: an isolated job ran out of memory
)
```

All derive from `TemplateError`, so `errors.Is(err, jaml.TemplateError)` matches any of them, and both sandbox kinds return the same kind and the same message for the same failure. An error also wraps its cause: a cancelled call matches `context.DeadlineExceeded` (or `context.Canceled`, or your `context.Cause`), a failed decode reaches `encoding/json`'s error, and a throw reaches `*jaml.Thrown`. Messages lead with the location, for example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: template error
```

A key longer than 60 characters appears in a location as its first 60 followed by `…`. Messages stay on one line and stay short: quoted code is collapsed to single spaces and cut at 60 characters, a thrown message at 200, and a syntax error gives its line and column in characters and quotes the code from that point. A getter that throws while jaml reads a result names where it was: `reading result.a.b: Error: x`.

## License

See [LICENSE](LICENSE).
