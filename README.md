# jaml

YAML templates whose values can hold JavaScript expressions, evaluated against your variables when you decode. jaml parses the YAML into plain data once, walks it at `Eval` time replacing every expression with its result, then hands the data to `encoding/json` to decode into your type.

## Install

```sh
go get github.com/gleno/jaml
```

## What it gives you

- **Two expression forms.** `${ … }` as a whole value is a typed result (a number stays a number, a list stays a list). `${{ … }}` inside text interpolates.
- **Nothing runs until the data arrives.** Making a template parses the YAML and compiles every expression, so syntax errors surface immediately, but evaluates nothing.
- **Results are data.** An expression must produce JSON data: scalars, lists, objects. Functions, symbols, BigInts, proxies, cycles, `NaN`, `Date`s, class instances, and strings or keys holding an unpaired surrogate are errors naming the path to the offending value.
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

`vars` must be JSON data. Every `Eval` checks and binds all of them before evaluating anything, whatever the path selects. A var that `encoding/json` cannot marshal, that nests deeper than 4096, that holds text or keys that are not valid UTF-8, or whose name is already a JavaScript global (`JSON`, `Math`, `globalThis`, `undefined`, `NaN`, …) is an `EvaluationFailed` error naming the var, never a template path.

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

- A value is a **whole expression** when, after trimming whitespace, it starts with `${`, ends with `}`, and the expression ends at that final `}`. Quoting does not matter: `${1+1}`, `"${1+1}"` and `'${1+1}'` are all the number 2.
- A value that starts with `${` but is not one whole expression (`${a} and ${b}`) is an error. Interpolate with `${{ }}` instead.
- **Interpolation** with `${{ … }}` always produces text, even when it is the whole value: `${{1+1}}` is `"2"`. To write an object literal as a whole value, put a space between the braces: `${ {a: 1} }`. Strings interpolate as they are; numbers, bools, lists and objects interpolate as JSON, keys sorted, written as `JSON.stringify` writes them: `${{ ({b: '<&>', a: 1}) }}` is `{"a":1,"b":"<&>"}`.
- An expression ends at the first closing `}` (or `}}` for interpolation) that JavaScript itself does not consume, so braces inside strings, template literals, regex literals and comments never close it: `${ /}/.test(x) }` and `${{ {a: 1}}}` work. A `//` comment runs to the end of its line, closing brace included; close the expression on the next line.
- A single `${` anywhere other than the start of a value is plain text: `echo ${HOME}` stays as written.
- To write the delimiters literally, use a JavaScript string: `${{'${{'}}` or `${'${x}'}`.
- **Keys are never evaluated.**
- An expression is exactly one strict-mode JavaScript expression, evaluated as a top-level script expression: statements (`1; 2`), legacy octal escapes (`"\101"`) and `new.target` are syntax errors, `arguments` is a `ReferenceError`, and `this` is `globalThis`. A body holding only comments is an empty expression. The vars are globals, deep-copied from `vars` the way `encoding/json` marshals them, so an expression cannot change your data. They never replace a built-in: a var named like one is refused. A var name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing var is a `ReferenceError`; a missing property of a var is `undefined`.
- All expressions in one `Eval` share one runtime, so one expression can see globals another one set. Separate `Eval` calls never share anything.

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

## Trusted and secure templates

goja, the JavaScript engine, cannot limit memory, and it cannot be interrupted inside a built-in call such as `'x'.repeat(1e9)`. So jaml does not pretend to bound an expression running in your process. You choose:

**`MakeTrustedTemplate`** runs expressions in your process, with no time or memory limit. An infinite loop hangs `Eval`; an allocation bomb takes the process down. Use it for templates you wrote. Untrusted *vars* are fine: they are bound as JSON data and never run. An expression that hands an untrusted string to `eval`, `new Function` or `new RegExp` runs it as code, and deep nesting in that string can still overflow goja's recursive parser; trusted mode makes no promise about running untrusted code.

**`MakeSecureTemplate(ctx, …)`** runs expressions in a child process that jaml starts and owns:

- **Construction.** Your process never interprets the template text: `MakeSecureTemplate` checks the options, starts the child and hands it the text, and the child parses the YAML and compiles every expression. Its answer ends construction. A template the child refuses fails with the same `InvalidTemplate` and the same message a trusted template would give; a path that is missing at `Eval` is the child's `MissingPath`, again word for word. So hostile text costs your process nothing to parse: construction is bounded by `ctx`, which kills the child and fails with `EvaluationFailed` matching the context's cause, and by the memory limit.
- **Memory.** The limit (`WithMemoryLimit`, 256 MiB by default, anywhere from 16 MiB to 1 TiB; any other value, 0 included, is `InvalidTemplate`) bounds two phases separately, each over a baseline the child measures itself, so heap that your packages build in their `init()` or package variables does not count against it. *Building* the template may grow the heap by at most the limit over what the binary holds when the child starts; this is the peak while parsing, several times the size of the built template, and a template that does not fit fails construction (or a later respawn) with `MemoryLimit`. *Each `Eval`* may then grow the heap by at most the limit over what the child holds once the template is built, so the template itself is not charged to every `Eval`; rendering plain data still counts, because the `Eval` allocates it. While it builds or serves, the child checks its heap every millisecond and exits once it passes its baseline plus the limit. One huge allocation is caught as soon as it is made; a doubling loop can reach about twice the limit before it is stopped. Either way the call fails with `MemoryLimit`, which is an `EvaluationFailed`, and "exceeded the memory limit". A child whose allocation the operating system refuses outright, so the Go runtime dies "out of memory", is a `MemoryLimit` too.
- **Time.** `Eval(ctx, …)` takes a context, and it bounds the whole call: waiting for a turn, starting a fresh child, the request and the answer. When it is done, the child is killed and `Eval` fails with `EvaluationFailed`. An `Eval` whose context is already done fails at once: it never waits for a turn, touches the child or starts a new one, and a child that is still starting when the context ends is killed.
- **Recovery.** A child that is killed or dies fails only the `Eval` it was serving; the next `Eval` starts a fresh one. A JavaScript error does not kill the child. A child that crashes is reported with its exit status and the reason line of its crash, never its goroutine dump.
- **Lifetime.** The child lives until `Close()` or until the template's context is done, whichever comes first, including a child that is still starting. `Close` never blocks. After that, `Eval` returns `Closed`. A template dropped without `Close` kills its child once the garbage collector finds it unreachable; `Close` is still the prompt way. A child also exits when its parent dies: at once when idle, within about a millisecond when busy.
- **One at a time.** `Eval` calls on one secure template take turns, so one bad input cannot fail another's work. Use several templates for parallelism. An idle child uses no CPU.
- **Clean environment.** The child gets none of your environment variables. It gets exactly `JAML_EVALUATOR`, `JAML_MEMORY_LIMIT`, `JAML_NONCE` (per start), `JAML_PARENT` (your process id), `ZONEINFO` when you have it set, and `TZ` when your local zone can be forwarded. A package whose `init()` needs another variable fails in the child, and every secure `Eval` fails with it.
- **Local time.** The child uses your local zone when it can find it the way you did: a zone from `TZ` at startup, by name or by the path of its file, or one you set with `time.Local, _ = time.LoadLocation(name)`, including one found through `ZONEINFO`. Then trusted and secure templates agree on local `Date` methods. A zone without such a name (`time.FixedZone`, a renamed or hand-built location) cannot be forwarded, so the child uses the system zone. The zone is read when a child starts; a later change to `time.Local` reaches only children started after it.
- **Signals.** The child runs in its own process group, so a terminal Ctrl-C, `SIGQUIT`, `SIGTSTP` or a signal sent to your process group does not reach it. It still dies with your process.
- **Cost.** Starting a child takes about 5 ms, and every child parses and compiles the template when it starts, at construction and at each respawn; a template that built once builds again, and a respawn that fails anyway reports the kind the child sent. A secure `Eval` is one round trip: the path and the vars go to the child, which selects and renders the subtree exactly as a trusted template would and sends back the data, which is then decoded into your target in your process.
- **Location.** An `Eval` that fails because the child was cancelled, ran out of memory or died names the selected path, not the expression that was running.

The child is your own binary, started again with a marker in its environment; on Linux it is the running image (`/proc/self/exe`), even if the file has been replaced since. jaml's `init()` sees the marker, identifies itself to the parent with a greeting that carries the protocol version and the nonce, serves evaluations over file descriptors 3 and 4, and exits, so your `main()` never runs in the child. The `init()` functions of packages that Go initializes before jaml do run there. A child that does not identify itself, because the executable is not a jaml binary or speaks another protocol version, is `Unsupported`, with a message naming which. Secure templates need a Unix that can start the binary again: they are not supported on Windows, iOS, js or wasip1 (`Unsupported`), or when jaml is built into a C shared library or a plugin.

Both kinds share the same limits:

- JavaScript calls nest at most 4096 deep; deeper recursion fails with `EvaluationFailed`.
- A value that holds code (a whole expression, or text with `${{`) is at most 65,536 bytes as written; a longer one is refused with `InvalidTemplate`, naming its path. This bounds how deep goja's recursive parser can go, so no template text can overflow the stack while it is compiled.
- Vars, results and templates nest at most 4096 deep. Deeper data is refused, naming the var or the path where it crosses the limit: a template with `InvalidTemplate`, a var or a result with `EvaluationFailed`.
- `JSON.parse` refuses text nested deeper than 4096 with a `RangeError`, so a deep string var cannot be turned into deep data.
- Data an expression builds for itself is not bounded. Some built-ins (`JSON.stringify`, `String` or `join` of an array, `flat`) walk it recursively on the Go stack, and a value nested about a million deep overflows that stack, which Go cannot recover from. It is the template's own risk, like an allocation bomb: a trusted template takes your process down, a secure template's child dies, and that `Eval` fails.
- A Go panic inside the engine, such as goja's `'x'.repeat(2 ** 52)`, fails the `Eval` with `EvaluationFailed` instead of reaching your code.
- A result may hold at most 1,048,576 values, each list and object counting as one. A bigger result is refused as a whole (`result holds more than 1048576 values`), before jaml allocates room for it, so a sparse list cannot claim more than it holds.
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
func WithMemoryLimit(bytes int64) Option             // secure only; 16 MiB to 1 TiB; default 256 MiB
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
	Unsupported      // secure templates on a platform that cannot run them, or a child that is not a jaml evaluator
	MemoryLimit      // derives from EvaluationFailed: a secure evaluator ran out of memory
)
```

All derive from `TemplateError`, so `errors.Is(err, jaml.TemplateError)` matches any of them, and trusted and secure templates return the same kind for the same failure. An error also wraps its cause: a secure `Eval` that ran out of time matches `context.DeadlineExceeded` (or `context.Canceled`, or your `context.Cause`), and a failed decode reaches `encoding/json`'s error. No JavaScript value escapes an `Eval`: what an expression threw is described as text when it is caught, so reading an error never runs template code. Messages lead with the location, for example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: template error
```

A key longer than 60 characters appears in a location as its first 60 followed by `…`. Messages stay on one line and stay short: quoted code is collapsed to single spaces and cut at 60 characters, a thrown message at 200, and a syntax error gives its line and column in characters and quotes the code from that point. A getter that throws while jaml reads a result names where it was: `reading result.a.b: Error: x`.

## License

See [LICENSE](LICENSE).
