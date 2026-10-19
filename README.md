# jaml

YAML documents whose values can hold JavaScript, resolved into your Go types. jaml parses a document into an `Expression` once. `Resolve` walks any part of it, evaluates the code it finds, calls the functions that code produces with your arguments, and hands the resulting data to `encoding/json` to decode into your type. A single JavaScript expression works the same way.

## Install

```sh
go get github.com/gleno/jaml
```

## Quick start

Make a sandbox and an expression once, on a long-lived context. Resolve it per request, with that request's values and scripts on the context:

```go
type Settings struct {
	Team     string           `json:"team"`
	Greeting string           `json:"greeting"`
	Limit    *jaml.Expression `json:"limit"`
}

sandbox, err := jaml.MakeSecureSandbox(appCtx)
if err != nil {
	return err
}
document, err := sandbox.MakeExpressionFromJaml(appCtx, `
team: ${ team.toUpperCase() }
greeting: "${{ greet(user) }}, welcome"
limit: "${ (plan) => plan.seats * 10 }"
`)
if err != nil {
	return err
}

ctx := jaml.WithValues(requestCtx, map[string]any{"team": "sales", "user": "ann"})
ctx = jaml.WithScript(ctx, `function greet(name) { return 'hi ' + name }`)
settings, err := jaml.Resolve[Settings](ctx, document)
// settings.Team == "SALES", settings.Greeting == "hi ann, welcome"
limit, err := jaml.Resolve[int](ctx, settings.Limit, map[string]any{"seats": 3})
// limit == 30
```

`${ … }` keeps its result's type, `${{ … }}` writes its result into text, and code that produces a function is called with the arguments of the `Resolve`. A `*jaml.Expression` field is left unevaluated, so you resolve it later with its own arguments. The rest of this README is the exact rules.

## Lifetimes are contexts

Nothing in jaml has a `Close` method. **Everything lives exactly as long as the context you made it with.**

- A `Sandbox` lives until the context passed to `MakeSecureSandbox` or `MakeTrustedSandbox` ends. That ends every expression in it, and a secure sandbox's child process.
- An `Expression` lives until the context passed to `MakeExpressionFromJaml` or `MakeExpression` ends, or its sandbox ends. `At` views share the lifetime of the expression they came from. `Copy(ctx)` makes a new handle that lives until its own `ctx` ends.
- After that, every call returns `Closed`.

So **pass a long-lived context** to the constructors: your service's root context, not a request's. An expression made with a request-scoped context dies when the request ends; `Copy` it onto a longer context if it must outlive the request. Each `Resolve` takes its own context, which bounds only that call.

An expression you drop without ending its context is still forgotten: once the garbage collector finds no handle to it reachable, jaml ends its lifetime and, when no other handle shares its data, tells the sandbox to drop it.

## What it gives you

- **One type, one verb.** A document, any part of it, and a lone JavaScript expression are all an `*Expression`. `Resolve[T](ctx, e, args...)` turns one into a `T`.
- **Two kinds of code.** `${ … }` as a whole value is **value code**: its result keeps its type (a number stays a number, a list stays a list). `${{ … }}` inside text is **interpolation**: its result becomes text.
- **Functions are called.** Code that evaluates to a function is called with the `args` of the `Resolve`; a function that returns a function is called again, with no arguments.
- **Nothing runs until you resolve.** Making an expression parses the YAML and compiles all its code, so syntax errors surface immediately, but evaluates nothing.
- **A pinned order.** Code runs in the order the document is written, so scripts with state behave the same every time.
- **Results are data.** Code must produce JSON data: scalars, lists, objects. Symbols, BigInts, proxies, cycles, `NaN`, `Date`s, class instances (an array or object counts as data only with its plain prototype or none), functions inside data, and strings or keys holding an unpaired surrogate are errors naming the path to the offending value.
- **Decoding is `encoding/json`.** Struct tags, embedded structs, `json.Unmarshaler`, `json.RawMessage`: whatever `json.Unmarshal` does, jaml does. A field typed `*jaml.Expression` is not evaluated: it receives a view you resolve later.
- **The world of an evaluation travels on the context.** Values, scripts, the clock and the random seed.
- **Thrown values as data.** What code threw is reachable as `*jaml.Thrown`, with its message and a JSON snapshot of the value.
- **Secure or trusted, chosen up front.** A secure sandbox runs everything in one child process with a memory ceiling, and survives anything a document does. A trusted sandbox runs in your process.
- **Safe to share.** Sandboxes and expressions are safe for concurrent use from any number of goroutines.

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

sandbox, err := jaml.MakeSecureSandbox(appCtx, jaml.WithMemoryLimit(512<<20))
if err != nil {
	return err
}
document, err := sandbox.MakeExpressionFromJaml(appCtx, text)
if err != nil {
	return err
}

ctx := jaml.WithValues(requestCtx, map[string]any{"name": "abc"})
cfg, err := jaml.Resolve[Config](ctx, document.At("config"))
// cfg == Config{Value1: 123, Value2: 2, Value3: "some string abc"}
```

### Paths

`At(path...)` selects a part of an expression, one key or list index per step (`"config"`, `"servers", "0"`), and returns a new `*Expression` for it. It never fails: a path that does not exist is a `MissingPath` error from `Resolve`, and so is a step that is not valid UTF-8, named after the steps before it (`$.k has no key "\xff"`). A list index is written in plain decimal, with no sign or leading zero. Only the selected part is evaluated. `At` navigates the document as written, never what code computes: a step into a value that holds code is a `MissingPath` error (`$.limit is code; At cannot look inside what it produces`), so resolve that value and index the result in Go. Error locations spell the selected path the way you asked for it: a step in plain decimal is an index (`$[0]`), even when it selects a mapping key `"0"`.

### Resolve

```go
func Resolve[T any](ctx context.Context, e *Expression, args ...any) (T, error)
```

`Resolve` walks the selected part depth-first: a mapping's keys in the order they are written, a list's items by index, several interpolations in one text left to right. An alias is evaluated again at every place it appears. Keys merged with `<<` are walked where the `<<` is written, except the keys the mapping writes itself, which stay where they are written. The order comes from the document, never from `T`.

At each piece of code:

1. The code is evaluated.
2. If the result is a function, it is called with `args`, with `this` set to `globalThis`.
3. While the result is still a function, it is called again with no arguments, so `(u) => () => lookup(u.id)` works.
4. At most 16 calls are made in all, the first included; a function still left after that is an `EvaluationFailed` error naming the path.

Code whose result is not a function ignores `args`. Each argument is passed as a fresh JSON copy for every call, so a function may change its arguments without affecting any other. Arguments must be JSON data, checked like values and named `argument 1`, `argument 2`, ….

```go
document, _ := sandbox.MakeExpressionFromJaml(appCtx, `
rename: "${ (state) => ({ ...state, name: 'bob' }) }"
team: ${ user.team }
`)
state, err := jaml.Resolve[State](ctx, document.At("rename"), State{Name: "alice"})
```

A Go type can accept more than one shape the usual way: give it an `UnmarshalJSON` that accepts both a string and a list, and `Resolve` into it.

### Expressions held for later

A field of `T` typed `*jaml.Expression`, a value of a `map[string]*jaml.Expression` or an item of a `[]*jaml.Expression` is not evaluated. `Resolve` skips that part of the document and sets the field to a view of it, as if by `At`, with the lifetime of the expression you resolved. Resolve it later with other arguments:

```go
type Config struct {
	Team  string           `json:"team"`
	Names *jaml.Expression `json:"names"`
}

config, err := jaml.Resolve[Config](ctx, document.At("config"), user)
names, err := jaml.Resolve[[]string](ctx, config.Names, other)
```

Fields are matched to the document the way `encoding/json` matches them: tags, embedded structs, `-`, and case-insensitive names. Where the document holds that path, the field gets a view, even when the value there is null: a literal is a constant expression, so that view resolves to null. Where the document lacks the path, the field stays nil. Code never fills such a field: where code produces null there, the field stays nil, and any other value is a `CoercionFailed` error at that path. A type that decodes itself (`json.Unmarshaler`, `encoding.TextUnmarshaler`) and `any` never skip anything. `T` never sees the skipped part. A `jaml.Expression` held by value rather than by pointer is an `InvalidTarget` error.

### Copies

```go
kept, err := document.At("config").Copy(processCtx)
```

`Copy(ctx)` makes a new handle on the same compiled document in the sandbox, alive until `ctx` ends or the sandbox does. A copy of an `At` view reaches only that part. Copying an expression whose lifetime has already ended is a `Closed` error.

### Values

```go
ctx = jaml.WithValues(ctx, map[string]any{"user": user, "limit": 10})
```

Values are globals for every piece of code in a `Resolve`, also reachable as `this.user` and `globalThis.user`. `WithValues` merges by name: a later binding of a name wins. The map is copied when you call `WithValues` (a shallow copy), and each value is encoded as JSON when `Resolve` runs, so code never sees or changes your data.

Every piece of code sees the values exactly as they were bound, whatever other code did: values are bound once per `Resolve` and deeply frozen. A value cannot be replaced or deleted, and changing it in place throws a `TypeError` that says so: `TypeError: cannot change a read-only value: …`. That covers an assignment, `delete`, `++` or `--` written in the expression itself, and a mutating array method (`sort`, `reverse`, `fill`, `copyWithin`, `splice`, `push`, `pop`, `shift`, `unshift`) or `Object.defineProperty`, `defineProperties`, `assign` and `setPrototypeOf` called from anywhere. A write to any other read-only object, a built-in included, gets the same message; an assignment inside a script's function keeps the engine's own message, unless it overrides a built-in property (below). Copy before you change: write `[...items].sort()` or `items.toSorted()` (the engine has `toSorted`, `toReversed`, `toSpliced` and `with`), and `{ ...user, name: 'x' }` rather than `user.name = 'x'`.

The built-ins are frozen too, after the scripts run and before the first piece of code, so no piece of code can change what a later one sees through a prototype or a built-in: `Object.prototype.admin = true` throws a `TypeError`, and so does redefining any built-in method, changing a built-in function such as `Function.prototype.call`, or replacing a built-in global such as `Math`. Everything a script added to the built-ins before the freeze is frozen with them. An object may still give itself its own `name`, `message`, `toString`, `valueOf`, `constructor`, `push` and the like by plain assignment (`Object.assign(new Error(), { name: 'Custom' })`, `o.toString = () => 'x'`): those inherited properties accept an assignment on any object but their own prototype. Overriding one on a frozen or non-extensible object, or on its prototype, always throws the read-only `TypeError`, even from `Reflect.set` or sloppy-mode code (`Function`, `eval`), where plain JavaScript would return `false` or do nothing. Every `Resolve` gets a fresh runtime, so nothing survives from one `Resolve` to the next. Freezing costs about 0.6 ms per `Resolve` that runs code, and about 3 ms when the `Resolve` runs scripts, because a script may have changed any built-in.

Values must be JSON data. Every `Resolve` checks and binds all of them before evaluating anything, whatever the path selects. A value that `encoding/json` cannot marshal, that nests deeper than 4096, that holds text or keys that are not valid UTF-8, or whose name is already a JavaScript global (`JSON`, `Math`, `globalThis`, `undefined`, `NaN`, …, and every name the global object inherits: `constructor`, `hasOwnProperty`, `__proto__`, …) is an `EvaluationFailed` error naming the value, located at the root `$`, never at the path you selected. Text is checked as JavaScript will see it, whatever marshalled it: a `json.RawMessage` or a custom `MarshalJSON` that writes an unpaired surrogate escape (`"\ud800"`) or raw bytes that are not UTF-8 is refused too. `encoding/json` writes invalid UTF-8 in a Go string as the escape `�`, so that escape is refused wherever it appears; a real U+FFFD written as the character itself binds. A value name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing value is a `ReferenceError`; a missing property of a value is `undefined`.

### Scripts

```go
ctx = jaml.WithScript(ctx, `
const rates = { eur: 0.92 }
function money(n) { return n.toFixed(2) }
`)
total, err := jaml.Resolve[string](ctx, document.At("total")) // total: ${{ money(order.amount * rates.eur) }}
```

A script is JavaScript statements, in strict mode. `WithScript` calls add up, in order. Each `Resolve` runs its scripts once, in order, before its first piece of code, and only for that `Resolve`. A script runs before the built-ins freeze, so it may extend them (`Array.prototype.total = …`) for the code of its `Resolve`. Every top-level declaration of a script (`var`, `let`, `const`, `function`, `class`, destructuring included) is visible to all the code of that `Resolve`, including functions it calls, and it is live: code that assigns to a script's `let` changes it for the code that runs after. That is why the order of evaluation is pinned. A later script that declares a name an earlier one declared wins.

Values shadow script declarations of the same name, silently: code sees the value, while the script's own functions still see their own declaration. A script may hold at most 65,536 bytes. A script with a syntax error is an `InvalidExpression` error naming it (`script 2: syntax error at line 1, column 5 …`), and a script that throws fails the `Resolve` with an `EvaluationFailed` error naming it, wrapping its `*jaml.Thrown`. A script runs even when the selected part holds no code. A document can carry its own script as text:

```go
script, err := jaml.Resolve[string](ctx, document.At("script"))
ctx = jaml.WithScript(ctx, script)
```

### Single expressions

```go
check, err := sandbox.MakeExpression(appCtx, "x => x.end >= x.start || 'end before start'")
verdict, err := jaml.Resolve[any](ctx, check, map[string]any{"start": 2, "end": 1})
// verdict == "end before start"

sum, err := sandbox.MakeExpression(appCtx, "a + b")
total, err := jaml.Resolve[int](jaml.WithValues(ctx, map[string]any{"a": 1, "b": 2}), sum)
// total == 3
```

`MakeExpression` takes one JavaScript expression, under the same rules as value code in a document, without the `${ }`. It resolves like a document whose root is that one piece of code. Errors locate it at `$`: `$: x => x.y.z: TypeError: …`. Results follow the same coercion options as documents, with the same defaults: null is `Loose` and undefined is `Strict`, so a validator that returns `undefined` for "no error" wants `jaml.WithUndefinedCoercion(jaml.Loose)`.

```go
check, err := sandbox.MakeExpression(specCtx, source, jaml.AsFunction())
```

`jaml.AsFunction()` makes `MakeExpression` accept only a function literal: an arrow function or a function expression, named or not, optionally in parentheses. Anything else is an `InvalidExpression` error, decided on the parsed syntax alone, so nothing is evaluated and the check costs no more than compiling: an identifier bound to a function (`parseInt`), a call (`(() => f)()`), an object, a class, or a sequence (`x => x, 1`) is "not a function literal". Async functions and generators are refused too, because what they return is a promise or an iterator, never data. Both sandbox kinds give the same answer. `MakeExpressionFromJaml` refuses the option with `InvalidExpression`.

### Sigil

```go
workflow, err := sandbox.MakeExpressionFromJaml(appCtx, text, jaml.WithSigil("@"))
```

`WithSigil` changes the `$` that opens code: with `@`, value code is `@{ … }` and interpolation is `@{{ … }}`, so a document can carry `${{ github.sha }}` as plain text. A sigil is not empty and holds no brace and no whitespace; the default is `$`. The braces never change. `MakeExpression` refuses the option. YAML reserves `@` at the start of a plain value, so quote a value that starts with it: `count: "@{ 1 + 1 }"`.

### Clock and seed

```go
ctx = jaml.WithFixedClock(ctx, time)
ctx = jaml.WithRandomSeed(ctx, seed)
```

Every `Resolve` pins a clock and a random seed when it is submitted: the ones on its context, or else the current time and a fresh random seed. `Date.now()` and `new Date()` return the pinned time for the whole call, and `Math.random()` draws from a generator seeded with the pinned seed. With both set, results are identical in both sandbox kinds and on every run. The clock is pinned to the millisecond, the way JavaScript sees it: any instant is accepted, its zone and anything below a millisecond are dropped, and an instant beyond about 292 million years from 1970 is held at that bound, the same in both kinds. Past the JavaScript `Date` range (±8.64e15 ms) `Date.now()` still returns the pinned number, and `new Date()` is an `Invalid Date`. Pinning at submission also means that a call replayed after a crash (see below) sees exactly what its first attempt saw.

### Thrown values

```go
var thrown *jaml.Thrown
if errors.As(err, &thrown) {
	_ = thrown.Message // "TypeError: no" or "threw an object", collapsed and cut at 200 characters
	_ = thrown.Value   // a JSON snapshot of the thrown value
}
```

An error from code that threw wraps a `*jaml.Thrown`, inside an `EvaluationFailed`. `Value` is what the thrown value looks like as JSON, read without calling `toJSON`: numbers are `float64`, lists `[]any`, objects `map[string]any`. Inside objects, functions, symbols and `undefined` are left out; inside lists they become `nil`. `NaN`, infinities and BigInts become `nil`. An `Error` or class instance contributes its own enumerable properties (`e.status = 404` gives `{"status": 404}`). Cycles, proxies and anything nested deeper than 4096 become `nil`. A string with an unpaired surrogate becomes `nil`, and a key with one is left out. If reading the value throws, or it holds more than 1,048,576 values, counting the value itself and every listed element and key once (left-out keys included), `Value` is `nil`: the same limit a result has. Getters run while it is read, inside the sandbox. Once the call's context has ended, the thrown value is not read; the call fails with the context's cause. No JavaScript value ever reaches your code.

## Syntax

```yaml
port: ${base + 1}                    # value code: typed result
url: "https://${{host}}:${{port}}/"  # interpolation: always text
limits: "${ {cpu: 2, memory: '1Gi'} }"
steps: |
  ${
    items
      .filter(item => item.enabled)
      .map(item => item.name)
  }
```

- A value is **value code** when, after trimming whitespace, it starts with `${`, ends with `}`, and the code ends at that final `}`. Quoting does not matter: `${1+1}`, `"${1+1}"` and `'${1+1}'` are all the number 2.
- A value that starts with `${` but is not one piece of value code (`${a} and ${b}`) is an error. Interpolate with `${{ }}` instead.
- **Interpolation** with `${{ … }}` always produces text, even when it is the whole value: `${{1+1}}` is `"2"`. To write an object literal as value code, put a space between the braces: `${ {a: 1} }`. Strings interpolate as they are; numbers, bools, lists and objects interpolate as JSON, keys sorted, written as `JSON.stringify` writes them: `${{ ({b: '<&>', a: 1}) }}` is `{"a":1,"b":"<&>"}`. A function in an interpolation is called like any other.
- Code ends at the first closing `}` (or `}}` for interpolation) that JavaScript itself does not consume, so braces inside strings, template literals, regex literals and comments never close it: `${ /}/.test(x) }` and `${{ {a: 1}}}` work. A `//` comment runs to the end of its line, closing brace included; close the code on the next line.
- A single `${` anywhere other than the start of a value is plain text: `echo ${HOME}` stays as written.
- To write the delimiters literally, use a JavaScript string: `${{'${{'}}` or `${'${x}'}`, or choose another sigil.
- **Keys are never evaluated.**
- Each piece of code is exactly one strict-mode JavaScript expression, evaluated as a top-level script expression: statements (`1; 2`), legacy octal escapes (`"\101"`) and `new.target` are syntax errors, `arguments` is a `ReferenceError`, and `this` is `globalThis`.
- All code in one `Resolve` shares one runtime, so a piece of code can see globals that earlier code set. Separate `Resolve` calls never share anything.
- `Date` and `Math.random` are pinned per call: the clock stands still for the whole `Resolve`, and the random sequence comes from one seed. See [Clock and seed](#clock-and-seed).

### YAML gotchas

`go.yaml.in/yaml/v3` parses the YAML; jaml resolves every value itself, by the rules below, into JSON data.

- **Quote code that contains `: `, `#`, or start a flow collection.** `limits: ${ {cpu: 2} }` is a YAML error, because `: ` inside a plain scalar starts a mapping. Write `limits: "${ {cpu: 2} }"`. Inside flow collections (`[…]`, `{…}`) all code needs quotes. The same goes for `?:` ternaries.
- **Only `true` and `false` are bools**, in any of their YAML spellings (`True`, `FALSE`). `y`, `yes`, `on`, `n` stay text.
- **Text is UTF-8.** A document that is not valid UTF-8 is an `InvalidExpression` error (`yaml: invalid leading UTF-8 octet`), and so is the source of a single expression or a script (`syntax error at line 1, column 3 of the expression: Invalid UTF-8 character`). Both kinds refuse it before building anything.
- **Keys must be strings.** `1: x` or `true: x` is an `InvalidExpression` error naming the path; quote the key.
- **Duplicate keys are refused**, with the line of the first one. Keys are compared after they resolve, so `a` and `!!binary YQ==` are the same key.
- **`.inf` and `.nan` are refused**, since JSON cannot carry them, and so are numbers too large for a 64-bit float (`1e400`). Quote them to keep them as text.
- **Dates stay text.** `2026-10-18` is the string `"2026-10-18"`, as written. A `time.Time` target still parses an RFC 3339 date-time such as `2026-10-18T10:00:00Z`.
- **Numbers follow one grammar**, plain or tagged. Integers are decimal (`1000`), `0x1F`, `0o17` or `0b101`; floats are decimal with an optional fraction and exponent (`1.5`, `.5`, `1.`, `1e10`, `-2.5E-3`). Underscores may sit between two digits anywhere in a number, or between a base prefix and its first digit (`1_000`, `.5_0`, `1e1_0`, `0xdead_beef`, `0x_ff`), and are dropped. An underscore anywhere else in a spelling that is otherwise a number (`1_`, `1__0`, `0x__ff`, `1_.5`, `1e_3`) is an `InvalidExpression` error naming the path, never silent text. Uppercase prefixes (`0X1F`, `0B101`, `0O17`) are text, as in YAML 1.2, and so is anything starting with an underscore (`_1`). `!!int` takes the integer spellings and `!!float` the decimal ones, so `!!float 0x10` is an error.
- **Integers beyond int64 are text.** `123456789012345678901234567890` is the string `"123456789012345678901234567890"`, digits intact. Integers that fit int64 are numbers.
- **No silent octal.** `0644` is 420 in YAML 1.1 and 644 in YAML 1.2, so jaml keeps it as the text `"0644"`, and the same goes for every integer with a leading zero, underscores included (`0_644`, `-0_755`). Write `0o644` for octal or `644` for decimal; `!!int 0644` is octal.
- Anchors, aliases and merge keys (`<<`) expand. A merge key takes a mapping or a list of mappings; the mapping's own keys win, then earlier mappings in the list win over later ones. Errors inside merged or aliased values name the path where the value lands, such as `$.x.a`. Expanded data counts toward the 4096 depth limit, and aliasing is bounded the way `go.yaml.in/yaml/v3` bounds it: once more than 100 aliased values and 1000 values in all have been read, aliased values may make up at most 99% of the values read, falling linearly from 400,000 values to 10% at 4,000,000. On top of that, aliases may expand to at most 1,048,576 values. Excessive aliasing is an `InvalidExpression` error naming the path where it tipped over.
- Tags are applied and then forgotten: `!!str 12` is the text `"12"`, `!!float 3` the number 3. A tag jaml does not know keeps the value as text. `!!binary` is base64 that must decode to UTF-8 text, which becomes the value; other bytes are an `InvalidExpression` error naming the path.
- **One YAML document per jaml document.** A second document (`---` followed by content) is an error.

## Decoding

After evaluation the selected subtree is plain JSON data, decoded with `json.Unmarshal`. A value that does not fit its target, such as text into an `int` field, is a `CoercionFailed` error that leads with the path of the value that did not fit (`$.servers[1].port`) and wraps `encoding/json`'s error, so `errors.As` reaches a `*json.UnmarshalTypeError` or the error your own `UnmarshalJSON` returned. There is no extra coercion: a number into a `string` field is an error, so interpolate (`"${{port}}"`) when you want text. Numbers into `any` are `float64`. Each `Resolve` decodes into a new zero `T`. A mapping reaches `encoding/json` with its keys in the order the document writes them, so when several keys match one field (`run` and `Run` both match `Run`), the one written last wins, for a `*jaml.Expression` field too.

## Null and undefined

YAML `~`, `null` and empty values are null and decode as JSON null. The settings apply to what code produces:

| | `Loose` | `Strict` |
| --- | --- | --- |
| null | JSON null | an error |
| undefined | JSON null | an error |

**Defaults:** null is `Loose`, undefined is `Strict` (it usually means a typo).

- `${{ }}` of null or undefined interpolates `""` when loose, and fails when strict.
- Inside a returned list, `undefined` becomes `null`; inside a returned object, a key holding `undefined` is dropped, as `JSON.stringify` does.
- JSON null leaves a non-pointer field as `encoding/json` leaves it: unchanged.

## Sandboxes

goja, the JavaScript engine, cannot limit memory, and it cannot be interrupted inside a built-in call such as a backtracking regular expression or `'x'.repeat(1e9)`. So jaml offers two kinds of sandbox with the same API, and expressions behave identically in both apart from the isolation guarantees.

```go
func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)

func WithMemoryLimit(bytes int64) SandboxOption        // secure only; 16 MiB to 1 TiB; default 256 MiB
func WithMaxConcurrency(jobs int) SandboxOption        // at least 1; default GOMAXPROCS
func WithBuildTimeout(timeout time.Duration) SandboxOption // positive; default 5s
```

Both kinds run at most `WithMaxConcurrency` jobs at once, in arrival order; a job is making an expression or a `Resolve`. `WithBuildTimeout` bounds making itself, not the time a job waits for its slot; the context bounds both. A build whose context or timeout ends stops between YAML nodes and pieces of code, and the call returns at once with the cause. A build that finishes as its timeout ends keeps whichever came first, in both kinds. A build that finishes after its context or its sandbox's context ended still fails with that cause; an expression is never handed back already closed. A bad option is an `InvalidExpression` error.

### Trusted

`MakeTrustedSandbox` runs everything in your process. The context of each call interrupts its JavaScript cooperatively, so an infinite loop ends with its context, and so does binding values or reading a result or a thrown value; but a built-in that never returns to the interpreter runs on, and an allocation bomb takes your process down. A build that outlives its context or timeout returns to you at once, and keeps its concurrency slot until it has stopped. `WithMemoryLimit` is refused. Use it for documents you wrote. Untrusted *values* and *arguments* are fine: they are bound as JSON data and never run. Code that hands an untrusted string to `eval`, `new Function` or `new RegExp` runs it as code; trusted mode makes no promise about running untrusted code.

### Secure

`MakeSecureSandbox` starts one child process that jaml owns and runs every expression of the sandbox in it, many at once, each job in its own fresh JavaScript runtime.

- **Building happens in the child.** Your process never interprets document or code text: it hands the text to the child, which parses and compiles it. A refusal comes back with the same kind and message a trusted sandbox gives. Your process keeps the text of each live expression, to build it again in a fresh child when it is next used.
- **Memory.** One ceiling: the child's live heap may grow at most `WithMemoryLimit` over what it held when it started, so heap that your packages build in `init()` does not count. Built expressions are resident and count. While any job runs, the child checks its heap every millisecond; past the ceiling it collects garbage once, and if the live heap is still over, it exits and the job that caused it fails with `MemoryLimit` ("exceeded the memory limit of N bytes"), which is an `EvaluationFailed`. A doubling loop can reach about twice the limit before it is stopped. A child whose allocation the operating system refuses outright is a `MemoryLimit` too. A child killed from outside jaml, usually by the kernel's out-of-memory killer, is a `MemoryLimit` when its live heap had passed three quarters of the ceiling; otherwise it fails with "evaluator died (signal: killed): killed from outside jaml", naming its peak resident memory on Linux.
- **Time.** When a call's context ends, the call returns at once. It fails with `EvaluationFailed` matching the context's cause (`context.DeadlineExceeded`, `context.Canceled` or your `context.Cause`) and names the selected path as you asked for it, whether the call was running or still waiting for its slot. The child interrupts the job, and the job keeps its concurrency slot until the child has let go of it. The child is the one that decides a job is stuck: a job still inside a built-in 250 ms after the child is told to stop it makes the child exit ("a job stuck after cancel"), and its other jobs are replayed as below. Only running counts: a job that has finished computing and is still sealing or sending its answer is never stuck, however big the answer or long the queue behind other answers. A build whose context ends is stopped by the child at its next YAML node or piece of code, never by an exit. For a build, only `WithBuildTimeout` can end the child: when one parse or compile step outlives it by 250 ms, the child exits the same way. As a backstop for a child that is wedged as a whole, your process kills it when it has sent nothing for 5 s after being told to stop a job ("the evaluator stopped answering"), or when it has taken none of a request for 5 s after that call's context ended ("not reading requests"); either way its other jobs are replayed as below. A call whose context is already done fails at once, without touching the child.
- **Recovery.** When the child dies for any reason, every job it was running is a suspect. jaml replays the suspects one at a time, each in a fresh child of its own that holds nothing but that job's expression, with its pinned clock, seed, values, scripts and arguments, while new jobs wait. A suspect that kills the child while running alone fails with its real cause: `MemoryLimit`, or "evaluator died" with its exit status and the reason line of its crash, never its goroutine dump. Every other suspect returns its normal result, as if nothing had happened. Then the sandbox runs jobs concurrently again, in another fresh child. A child that cannot be started fails every job waiting for it, once, with the start error; the next call tries again. A job whose own deadline forced the kill is not replayed; it fails with its context's cause. jaml does not judge: it never flags or refuses an expression, and an expression whose `Resolve` ran out of memory once is tried again on its next `Resolve`. Jobs that only overflowed together all succeed on replay. Replay is safe because jobs are pure: values, scripts and arguments are part of the job, values and arguments are JSON copies, and there is no I/O and no callback into your process. A replay runs the scripts again.
- **Replies.** Each answer travels back to your process as one message of at most a quarter of `WithMemoryLimit` (64 MiB by default), whatever it holds: data, a thrown value or a failure. A bigger one is refused in the child with `EvaluationFailed` ("$.path: the answer is larger than N bytes"); control characters and the line and paragraph separators U+2028 and U+2029 count as the six bytes they take escaped. So your process never holds more than that per running job. A trusted sandbox has no such cap. A child that sends a bigger message anyway is killed, and its jobs see that as its death.
- **Clean environment.** The child gets none of your environment variables. It gets exactly `JAML_EVALUATOR`, `JAML_MEMORY_LIMIT`, `JAML_NONCE` (per start), `JAML_PARENT` (your process id), `ZONEINFO` when you have it set, and `TZ` when your local zone can be forwarded. A package whose `init()` needs another variable fails in the child.
- **Local time.** The child uses your local zone when it can find it the way you did: a zone from `TZ` at startup, by name or by the path of its file, or one you set with `time.Local, _ = time.LoadLocation(name)`, including one found through `ZONEINFO`. Then trusted and secure sandboxes agree on local `Date` methods. A zone without such a name (`time.FixedZone`, a renamed or hand-built location) cannot be forwarded, so the child uses the system zone. The zone is read when a child starts.
- **Signals and parents.** The child runs in its own process group, so a terminal Ctrl-C or a signal sent to your process group does not reach it. It exits when your process dies: at once when idle, within about a millisecond when busy. An idle child uses no CPU.
- **Cost.** Starting a child takes about 5 ms. A start, first or after a death, is bounded by the sandbox's context and by a fixed 30 s, sized for a loaded machine; never by `WithBuildTimeout`, which bounds builds only, and never by the call that needed it, which stops waiting when its own context ends. Recovery costs one start per suspect. A `Resolve` is one request to the child, which sends back the data, decoded into your target in your process. Each distinct script is compiled once and kept in a bounded cache.

The child is your own binary, started again with a marker in its environment; on Linux it is the running image (`/proc/self/exe`), even if the file has been replaced since. jaml's `init()` sees the marker, identifies itself to the parent with a greeting that carries the protocol version and the nonce, serves over file descriptors 3, 4 and 5, and exits, so your `main()` never runs in the child. The `init()` functions of packages that Go initializes before jaml do run there. A child that answers with something other than its greeting, because the executable is not a jaml binary or speaks another protocol version, is `Unsupported`, with a message naming which. A child that dies before it greets, such as one whose `init()` panics, fails with `EvaluationFailed`, naming its exit status and the reason line of its crash. Secure sandboxes need a Unix that can start the binary again: they are not supported on Windows, iOS, js or wasip1 (`Unsupported`), or when jaml is built into a C shared library or a plugin.

### Limits

Both kinds share these limits:

- JavaScript calls nest at most 4096 deep; deeper recursion fails with `EvaluationFailed`.
- A value that holds code (value code, or text with `${{`) and a script are each at most 65,536 bytes as written; a longer one is refused with `InvalidExpression`, naming its path or the script. This bounds how deep goja's recursive parser can go, so no text can overflow the stack while it is compiled.
- Values, arguments, results and documents nest at most 4096 deep. Deeper data is refused, naming the value or the path where it crosses the limit: a document with `InvalidExpression`, a value, an argument or a result with `EvaluationFailed`.
- `JSON.parse` refuses text nested deeper than 4096 with a `RangeError`, so a deep string value cannot be turned into deep data.
- Data code builds for itself is not bounded. Some built-ins (`JSON.stringify`, `String` or `join` of an array, `flat`) walk it recursively on the Go stack, and a value nested about a million deep overflows that stack, which Go cannot recover from. It is the document's own risk, like an allocation bomb: a trusted sandbox takes your process down; a secure sandbox's child dies, and that `Resolve` fails with the cause once it is replayed alone.
- A Go panic inside the engine, such as goja's `'x'.repeat(2 ** 52)`, fails the `Resolve` with `EvaluationFailed` instead of reaching your code.
- A Go panic while building, such as goja's parser on `'\u{10FFFF}'`, refuses the expression with `InvalidExpression`, naming the value's path (`$` for a single expression); neither the host nor a secure sandbox's child goes down.
- A result may hold at most 1,048,576 values, each list and object counting as one. A bigger result is refused as a whole (`result holds more than 1048576 values`), before jaml allocates room for it, so a sparse list cannot claim more than it holds.
- The runtime has no I/O, no `require`, no timers, and source-map comments are not followed.

## API

```go
func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func (s *Sandbox) MakeExpressionFromJaml(ctx context.Context, jaml string, options ...Option) (*Expression, error)
func (s *Sandbox) MakeExpression(ctx context.Context, expression string, options ...Option) (*Expression, error)

func (e *Expression) At(path ...string) *Expression
func (e *Expression) Copy(ctx context.Context) (*Expression, error)
func Resolve[T any](ctx context.Context, e *Expression, args ...any) (T, error)

func WithValues(ctx context.Context, values map[string]any) context.Context
func WithScript(ctx context.Context, source string) context.Context
func WithFixedClock(ctx context.Context, time time.Time) context.Context
func WithRandomSeed(ctx context.Context, seed uint64) context.Context

func WithNullCoercion(coercion Coercion) Option      // Loose (default) or Strict
func WithUndefinedCoercion(coercion Coercion) Option // Loose or Strict (default)
func AsFunction() Option                             // MakeExpression only: refuse anything but a function literal
func WithSigil(sigil string) Option                  // MakeExpressionFromJaml only: what opens code; default "$"
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
	ExpressionError   // base sentinel for every jaml error
	InvalidExpression // bad YAML, bad JavaScript syntax, a bad script, bad options
	InvalidTarget     // T is something encoding/json cannot decode into, or holds a jaml.Expression by value
	MissingPath       // the path names nothing in the document
	EvaluationFailed  // code or a script threw or produced something that is not data; a call was cancelled; a secure sandbox's child ran out of memory or died
	CoercionFailed    // json.Unmarshal refused the data, or strict null/undefined refused a result
	Closed            // the expression's own context or its sandbox's has ended; the message says which
	Unsupported       // a secure sandbox on a platform that cannot run one, or a child that is not a jaml evaluator
	MemoryLimit       // derives from EvaluationFailed: a secure sandbox's job ran out of memory
)
```

All derive from `ExpressionError`, so `errors.Is(err, jaml.ExpressionError)` matches any of them, and both sandbox kinds return the same kind and the same message for the same failure. An error also wraps its cause: a cancelled call matches `context.DeadlineExceeded` (or `context.Canceled`, or your `context.Cause`), a failed decode reaches `encoding/json`'s error, and a throw reaches `*jaml.Thrown`. Every message leads with a location: `$` for the root, `$.a.b` deeper. An error about no particular place, such as a bad option or a cancelled build, is located at `$`. For example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: expression error
$.config: its own context, the one it was made or copied with, has ended: closed: expression error
```

A `Closed` error names which context ended: the expression's own (the one passed to `MakeExpression…` or `Copy`), or the sandbox's. A strict undefined result from an arrow whose body is a block adds a hint, since `() => { ok: true }` is a block with a label, not an object: write `() => ({ ok: true })`.

A key longer than 60 characters appears in a location as its first 60 followed by `…`. Messages stay on one line and stay short: quoted code is collapsed to single spaces and cut at 60 characters, a thrown message at 200, and a syntax error gives its line and column in characters and quotes the code from that point. A getter that throws while jaml reads a result names where it was: `reading result.a.b: Error: x`.

## License

See [LICENSE](LICENSE).
