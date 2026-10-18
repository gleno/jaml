# jaml

YAML templates whose values can hold JavaScript expressions, evaluated against your variables when you decode. jaml parses the YAML into plain data once, walks it at `Eval` time replacing every expression with its result, then hands the data to `encoding/json` to decode into your type.

## Install

```sh
go get github.com/gleno/jaml
```

## What it gives you

- **Two expression forms.** `${ … }` as a whole value is a typed result (a number stays a number, a list stays a list). `${{ … }}` inside text interpolates.
- **Nothing runs until the data arrives.** Making a template parses the YAML and compiles every expression, so syntax errors surface immediately, but evaluates nothing.
- **Results are data.** An expression must produce JSON data: scalars, lists, objects. Functions, symbols, cycles, `NaN`, `Date`s and class instances are errors naming the path to the offending value.
- **Decoding is `encoding/json`.** Struct tags, embedded structs, `json.Unmarshaler`, `json.RawMessage`: whatever `json.Unmarshal` does, jaml does.
- **Null and undefined on your terms.** `Loose` or `Strict` for expression results, configured separately.
- **Trusted or secure, chosen up front.** A trusted template runs expressions in your process with no limits. A secure template runs them in a child process with a memory limit, cancelled through `context.Context`.
- **Safe to share.** A `Template` is immutable; call `Eval` from as many goroutines as you like.

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

template, err := jaml.MakeTrustedTemplate(text)
if err != nil {
	return err
}

var cfg Config
err = template.Eval(&cfg, map[string]any{"name": "abc"}, "config")
// cfg == Config{Value1: 123, Value2: 2, Value3: "some string abc"}
```

`Eval(into, vars, path...)` decodes into the non-nil pointer `into`. The optional `path` selects a subtree first, one key or list index per element (`"config"`, `"servers", "0"`); only that subtree is evaluated. A list index is written in plain decimal, with no sign or leading zero. A path that does not exist is a `MissingPath` error.

`vars` must be JSON data. Every `Eval` checks all of them before evaluating anything, whatever the path selects; a var that `encoding/json` cannot marshal, or that nests deeper than 4096, is an `EvaluationFailed` error naming the var.

### Templates you do not trust

```go
template, err := jaml.MakeSecureTemplate(ctx, text, jaml.WithMemoryLimit(128<<20))
if err != nil {
	return err
}
defer template.Close()

evalCtx, cancel := context.WithTimeout(ctx, time.Second)
defer cancel()
err = template.Eval(evalCtx, &cfg, vars, "config")
```

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

- A value is a **whole expression** when, after trimming whitespace, it starts with `${`, ends with `}`, and is one balanced expression. Quoting does not matter: `${1+1}`, `"${1+1}"` and `'${1+1}'` are all the number 2.
- A value that starts with `${` but is not one whole expression (`${a} and ${b}`) is an error. Interpolate with `${{ }}` instead.
- **Interpolation** with `${{ … }}` always produces text, even when it is the whole value: `${{1+1}}` is `"2"`. To write an object literal as a whole value, put a space between the braces: `${ {a: 1} }`.
- A single `${` anywhere other than the start of a value is plain text: `echo ${HOME}` stays as written.
- To write the delimiters literally, use a JavaScript string: `${{'${{'}}` or `${'${x}'}`.
- **Keys are never evaluated.**
- Expressions run in strict mode. The vars are globals, deep-copied from `vars` the way `encoding/json` marshals them, so an expression cannot change your data. A var name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing var is a `ReferenceError`; a missing property of a var is `undefined`.
- All expressions in one `Eval` share one runtime, so one expression can see globals another one set. Separate `Eval` calls never share anything.

### YAML gotchas

YAML is read by `go.yaml.in/yaml/v3` into plain data, then checked to be JSON data.

- **Quote expressions that contain `: `, `#`, or start a flow collection.** `limits: ${ {cpu: 2} }` is a YAML error, because `: ` inside a plain scalar starts a mapping. Write `limits: "${ {cpu: 2} }"`. Inside flow collections (`[…]`, `{…}`) every expression needs quotes. The same goes for `?:` ternaries.
- **Only `true` and `false` are bools.** `y`, `yes`, `on`, `n` stay text.
- **Keys must be strings.** `1: x` or `true: x` is an `InvalidTemplate` error naming the path; quote the key.
- **Duplicate keys are refused**, with the line of the first one.
- **`.inf` and `.nan` are refused**, since JSON cannot carry them, and so are numbers too large for a 64-bit float (`1e400`). Quote them to keep them as text.
- **Dates stay text.** `2026-10-18` is the string `"2026-10-18"`, as written. A `time.Time` target still parses an RFC 3339 date-time such as `2026-10-18T10:00:00Z`.
- **Integers beyond int64 are text.** `123456789012345678901234567890` is the string `"123456789012345678901234567890"`, digits intact. Integers that fit int64 are numbers.
- **No silent octal.** `0644` is 420 in YAML 1.1 and 644 in YAML 1.2, so jaml keeps it as the text `"0644"`. Write `0o644` for octal or `644` for decimal; `!!int 0644` is octal.
- Anchors, aliases and merge keys (`<<`) expand. Tags are applied by the parser and then forgotten: `!!str 12` is the text `"12"`.
- **One document per template.** A second document (`---` followed by content) is an error.

## Decoding

After evaluation the selected subtree is plain JSON data, decoded with `json.Unmarshal`. A value that does not fit its target, such as text into an `int` field, is a `CoercionFailed` error carrying `encoding/json`'s message. There is no extra coercion: a number into a `string` field is an error, so interpolate (`"${{port}}"`) when you want text. Numbers into `any` are `float64`.

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

## Trusted and secure templates

goja, the JavaScript engine, cannot limit memory, and it cannot be interrupted inside a built-in call such as `'x'.repeat(1e9)`. So jaml does not pretend to bound an expression running in your process. You choose:

**`MakeTrustedTemplate`** runs expressions in your process, with no time or memory limit. An infinite loop hangs `Eval`; an allocation bomb takes the process down. Use it for templates you wrote. Untrusted *vars* are fine: they are bound as JSON data and never run. An expression that hands an untrusted string to `eval`, `new Function` or `new RegExp` runs it as code, and deep nesting in that string can still overflow goja's recursive parser; trusted mode makes no promise about running untrusted code.

**`MakeSecureTemplate(ctx, …)`** runs expressions in a child process that jaml starts and owns:

- **Memory.** The child watches its own heap every millisecond and exits once it passes the limit (`WithMemoryLimit`, 256 MiB by default). One huge allocation is caught before it is touched; a doubling loop can reach about twice the limit before it is stopped. On Linux an address-space rlimit of 8× the limit (at least 4 GiB) backs it up.
- **Time.** `Eval(ctx, …)` takes a context. When it is done, the child is killed and `Eval` fails with `EvaluationFailed`.
- **Recovery.** A child that is killed or dies fails only the `Eval` it was serving; the next `Eval` starts a fresh one. A JavaScript error does not kill the child.
- **Lifetime.** The child lives until `Close()` or until the template's context is done, whichever comes first. After that, `Eval` returns `Closed`. A child also exits when its parent dies.
- **One at a time.** `Eval` calls on one secure template take turns, so one bad input cannot fail another's work. Use several templates for parallelism.
- **Clean environment.** The child gets none of your environment variables, only its marker and its limit.
- **Cost.** Starting a child takes about 5 ms; a secure `Eval` adds about 50 µs of round trips per expression over a trusted one.

The child is your own binary, started again with a marker in its environment. jaml's `init()` sees the marker, serves evaluations over file descriptors 3 and 4, and exits, so your `main()` never runs in the child. The `init()` functions of packages that Go initializes before jaml do run there. Secure templates need a binary that can start itself again: they are not supported on Windows (`Unsupported`), or when jaml is built into a C shared library or a plugin.

Both kinds share the same limits:

- JavaScript calls nest at most 4096 deep; deeper recursion fails with `EvaluationFailed`.
- Vars, results and templates nest at most 4096 deep. Deeper data is refused, naming the var or the path where it crosses the limit: a template with `InvalidTemplate`, a var or a result with `EvaluationFailed`.
- `JSON.parse` refuses text nested deeper than 4096 with a `RangeError`, so a deep string var cannot be turned into deep data.
- Data an expression builds for itself is not bounded. Some built-ins (`JSON.stringify`, `String` or `join` of an array, `flat`) walk it recursively on the Go stack, and a value nested about a million deep overflows that stack, which Go cannot recover from. It is the template's own risk, like an allocation bomb: a trusted template takes your process down, a secure template's child dies, and that `Eval` fails.
- A Go panic inside the engine, such as goja's `'x'.repeat(2 ** 52)`, fails the `Eval` with `EvaluationFailed` instead of reaching your code.
- A result may hold at most 1,048,576 values.
- The runtime has no I/O, no `require`, no timers, and source-map comments are not followed.

## API

```go
func MakeTrustedTemplate(text string, options ...Option) (*TrustedTemplate, error)
func (t *TrustedTemplate) Eval(into any, vars map[string]any, path ...string) error

func MakeSecureTemplate(ctx context.Context, text string, options ...Option) (*SecureTemplate, error)
func (t *SecureTemplate) Eval(ctx context.Context, into any, vars map[string]any, path ...string) error
func (t *SecureTemplate) Close() error

func WithNullCoercion(coercion Coercion) Option      // Loose (default) or Strict
func WithUndefinedCoercion(coercion Coercion) Option // Loose or Strict (default)
func WithMemoryLimit(bytes int64) Option             // secure only; default 256 MiB
```

### Errors

```go
var (
	TemplateError    // base sentinel for every jaml error
	InvalidTemplate  // bad YAML, bad JavaScript syntax, bad options
	InvalidTarget    // into is not a non-nil pointer
	MissingPath      // the path names nothing in the template
	EvaluationFailed // an expression threw or produced something that is not data; a secure evaluator was cancelled, ran out of memory or died
	CoercionFailed   // json.Unmarshal refused the data, or strict null/undefined refused a result
	Closed           // Eval on a secure template after Close or after its context ended
	Unsupported      // secure templates on a platform that cannot run them
)
```

All derive from `TemplateError`, so `errors.Is(err, jaml.TemplateError)` matches any of them. Messages lead with the location, for example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: template error
```

## License

See [LICENSE](LICENSE).
