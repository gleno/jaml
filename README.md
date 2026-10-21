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

- A `Sandbox` lives until the context passed to its constructor (`MakeSecureSandbox`, `MakeSecureSandboxWithoutBuiltinsProtection` or `MakeTrustedSandbox`) ends. That ends every expression in it, and a secure sandbox's child process.
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
- **The world of an evaluation travels on the context.** Values, scripts, the clock, the random seed, the time limits, the meter and the console.
- **Thrown values as data.** What code threw is reachable as `*jaml.Thrown`, with its message and a JSON snapshot of the value.
- **Three sandboxes, chosen up front.** A secure sandbox runs everything in one child process with a memory ceiling, and survives anything a document does. `MakeSecureSandbox` also protects the pieces of code of a `Resolve` from each other by freezing the JavaScript built-ins and the values, so no piece of code can change what another sees; `MakeSecureSandboxWithoutBuiltinsProtection` leaves both ordinary and is cheaper per `Resolve`. A trusted sandbox runs in your process, unprotected.
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

Each value is bound the first time code reads it, once per `Resolve`, as a fresh copy for that `Resolve`, so a value no code reads costs nothing to bind and nothing code does to a value reaches the next `Resolve`. In every sandbox a value's name is read-only: the value cannot be replaced or deleted, and assigning to its name throws a `TypeError` that says so (`TypeError: cannot change a read-only value: …`), from anywhere, even through `Reflect.set` or sloppy-mode code (`Function`), where plain JavaScript would return `false` or do nothing. A value is a frozen accessor on the global object, enumerable and not configurable, so `Object.getOwnPropertyDescriptor(globalThis, 'user')` shows `get` and `set` rather than `value` and `writable`.

What lies inside a value follows protection. In a sandbox from `MakeSecureSandbox`, every piece of code sees the values exactly as they were bound, whatever other code did: each value is deeply frozen, and changing it in place throws a `TypeError` that says so: `TypeError: cannot change a read-only value: …`. That covers an assignment, `delete`, `++` or `--` written in the expression itself, and a mutating array method (`sort`, `reverse`, `fill`, `copyWithin`, `splice`, `push`, `pop`, `shift`, `unshift`) or `Object.defineProperty`, `defineProperties`, `assign` and `setPrototypeOf` called from anywhere. A write to any other read-only object, a built-in included, gets the same message; an assignment in a script, at its top level or inside its functions, keeps the engine's own message, unless it overrides a built-in property (below). These methods refuse from the moment the `Resolve` starts, so a script that sorts a value at its top level gets the same `TypeError`. Copy before you change: write `[...items].sort()` or `items.toSorted()` (the engine has `toSorted`, `toReversed`, `toSpliced` and `with`), and `{ ...user, name: 'x' }` rather than `user.name = 'x'`.

In a sandbox from `MakeSecureSandbox`, the built-ins are frozen too, after the scripts run and before the first piece of code, so no piece of code can change what a later one sees through a prototype or a built-in. Expressions and scripts are strict code, and there `Object.prototype.admin = true` throws a `TypeError`, and so does redefining any built-in method, changing a built-in function such as `Function.prototype.call`, or replacing a built-in global such as `Math`. Sloppy-mode code (`Function`, indirect `eval`) gets plain JavaScript's silent failure instead, and `Reflect.set` returns `false`: no error, and nothing changes. Everything a script added to the built-ins before the freeze is frozen with them. An object may still give itself its own `name`, `message`, `toString`, `valueOf`, `constructor`, `push` and the like by plain assignment (`Object.assign(new Error(), { name: 'Custom' })`, `o.toString = () => 'x'`): those inherited properties accept an assignment on any object but their own prototype. Overriding one on a frozen or non-extensible object, or on its prototype, always throws the read-only `TypeError`, even from `Reflect.set` or sloppy-mode code (`Function`, `eval`), where plain JavaScript would return `false` or do nothing. Freezing costs about 0.65 ms per `Resolve` that runs code, and about 2 ms when the `Resolve` runs scripts, because a script may have changed any built-in.

In a sandbox from `MakeSecureSandboxWithoutBuiltinsProtection` or `MakeTrustedSandbox`, the built-ins are ordinary JavaScript: nothing freezes them, and code may change them. A piece of code that sets `Object.prototype.admin = true`, replaces `Function.prototype.call` or assigns `Math = {}` changes what every later piece of code of the same `Resolve` sees, so the pieces of one `Resolve` can influence each other and the order of the document can matter. Values are ordinary JavaScript data too: `user.order.total = 0`, `items.sort()` and `items.push(4)` change the value in place, and every later piece of code of the same `Resolve` sees the change, while the next `Resolve` binds a fresh copy and never does. Only a value's name stays read-only, as above, and a write to an object that code froze itself keeps the engine's own message. Such a `Resolve` pays no freeze, of the built-ins or of the values: one that runs code costs about 0.01 ms in your process, scripts or not, and a value read once binds faster than a frozen one: a list of 1,000 small objects binds in about 1.1 ms rather than 1.7 ms, and with a nested list and object in each, about 2.5 ms rather than 4.8 ms.

Every `Resolve` gets a fresh runtime, in a sandbox from any of the three constructors (`MakeSecureSandbox`, `MakeSecureSandboxWithoutBuiltinsProtection` and `MakeTrustedSandbox`), so state never crosses calls: nothing survives from one `Resolve` to the next, a global a piece of code set and a change to a built-in included.

Values must be JSON data. Every `Resolve` checks all of them before evaluating anything, whatever the path selects. A value that `encoding/json` cannot marshal, that nests deeper than 4096, that holds text or keys that are not valid UTF-8, or whose name is already a JavaScript global (`JSON`, `Math`, `globalThis`, `undefined`, `NaN`, …, and every name the global object inherits: `constructor`, `hasOwnProperty`, `__proto__`, …) is an `InvalidInput` error naming the value, located at the root `$`, never at the path you selected. Text is checked as JavaScript will see it, whatever marshalled it: a `json.RawMessage` or a custom `MarshalJSON` that writes an unpaired surrogate escape (`"\ud800"`) or raw bytes that are not UTF-8 is refused too. `encoding/json` writes invalid UTF-8 in a Go string as the escape `\ufffd`, and once encoded the two cannot be told apart, so that escape is refused wherever it appears, including one your `json.RawMessage` or `MarshalJSON` wrote; a real U+FFFD written as the character itself binds, and so does the six-character text `\ufffd` in a Go string. A value name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing value is a `ReferenceError`; a missing property of a value is `undefined`.

### Scripts

```go
ctx = jaml.WithScript(ctx, `
const rates = { eur: 0.92 }
function money(n) { return n.toFixed(2) }
`)
total, err := jaml.Resolve[string](ctx, document.At("total")) // total: ${{ money(order.amount * rates.eur) }}
```

A script is JavaScript statements, in strict mode. `WithScript` calls add up, in order. Each `Resolve` runs its scripts once, in order, before its first piece of code, and only for that `Resolve`. A script runs before the built-ins freeze, so it may extend them (`Array.prototype.total = …`) for the code of its `Resolve`, in every sandbox. Every top-level declaration of a script (`var`, `let`, `const`, `function`, `class`, destructuring included) is visible to all the code of that `Resolve`, including functions it calls, and it is live: code that assigns to a script's `let` changes it for the code that runs after. That is why the order of evaluation is pinned. A later script that declares a name an earlier one declared wins.

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

`MakeExpression` takes bare code. When you hold value code as text, `${` and `}` included, as a YAML value would hold it, let jaml find where the code ends: marshal it into a one-key document and resolve that key. jaml's own scan decides where the code closes, so braces in strings, regex literals and comments never end it:

```go
written, err := yaml.Marshal(map[string]string{"v": "${p => /}/.test(p)}"})
document, err := sandbox.MakeExpressionFromJaml(appCtx, string(written))
check := document.At("v")
matched, err := jaml.Resolve[bool](ctx, check, "a}b") // true
```

A text that is not one piece of value code is refused the way a document refuses it, located at `$.v`.

`jaml.AsFunction()` makes `MakeExpression` accept only a function literal: an arrow function or a function expression, named or not, optionally in parentheses. Anything else is an `InvalidExpression` error, decided on the parsed syntax alone, so nothing is evaluated and the check costs no more than compiling: an identifier bound to a function (`parseInt`), a call (`(() => f)()`), an object, a class, or a sequence (`x => x, 1`) is "not a function literal". Async functions and generators are refused too, because what they return is a promise or an iterator, never data. Every sandbox gives the same answer. `MakeExpressionFromJaml` refuses the option with `InvalidExpression`.

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

Every `Resolve` pins a clock and a random seed when it is submitted: the ones on its context, or else the current time and a fresh random seed. `Date.now()` and `new Date()` return the pinned time for the whole call, and `Math.random()` draws from a generator seeded with the pinned seed. With both set, results are identical in every sandbox and on every run. The clock is pinned to the millisecond, the way JavaScript sees it: any instant is accepted, its zone and anything below a millisecond are dropped, and an instant beyond about 292 million years from 1970 is held at that bound, the same in every sandbox. Past the JavaScript `Date` range (±8.64e15 ms) `Date.now()` still returns the pinned number, and `new Date()` is an `Invalid Date`. Pinning at submission also means that a call replayed after a crash (see below) sees exactly what its first attempt saw.

### Time limit

```go
ctx = jaml.WithTimeLimit(ctx, 100*time.Millisecond)
ctx = jaml.WithWallLimit(ctx, time.Second)
```

`WithTimeLimit` bounds the time one `Resolve` spends computing: running its scripts, binding its values, reading its result and every piece of code it evaluates, all counted together from the moment the job starts running. It counts the job's own time, the CPU time of the thread that runs it: every job runs on an OS thread of its own for as long as it runs, so the time a sibling job, another process or the host's scheduling takes never counts, and an innocent job on a loaded host never runs out of it. Compiling its scripts never counts either: they are compiled before the clock starts, bounded by `WithBuildTimeout` and `WithBuildTimeLimit` like a build, so a script cache that is cold or warm gives the same outcome. Nor does waiting for a concurrency slot, for a secure sandbox's child to start, for an expression to be built again in a fresh child, or behind a replay. A `Resolve` that computes longer is interrupted and fails with `TimeLimit`, an `EvaluationFailed` naming the path (`$.check: ${…}: exceeded the time limit of 100ms`), so you can tell it from your context's deadline, which still bounds everything, waiting included. When a job is replayed after a crash (see Recovery), its replay runs from the start with the whole limit again. Setting it twice keeps the smaller limit, as deadlines do; a limit that is not positive is an `InvalidInput` error from `Resolve`. The limit is checked at least every millisecond, so a job stops within about a millisecond of it, later only when the host is too loaded to run the check. Own time is the thread's CPU time, so it still grows when the core is shared: beside jobs on the other hyperthreads of the same cores, or beside jobs that stream through memory, the same work takes up to about twice the CPU time it takes alone (measured on a 4-vCPU runner: 20 ms alone, 30 to 38 ms beside two spinning siblings, up to 51 ms beside allocating ones), while waiting for a core still never counts. The collector's pacing makes no difference to that. Size a budget from the work's cost alone with that headroom, and keep the wall limit as the backstop.

`WithWallLimit` bounds the same stretch of the job in wall time, with its own cause: a `Resolve` that is still running when it ends fails with `WallLimit` (`$.check: ${…}: exceeded the wall time limit of 1s`). Set both: a time limit sized for the script, and a wall limit a few times larger as a backstop. A job that fails with `TimeLimit` was running away; one that fails with `WallLimit` but not `TimeLimit` was starved, by the host or by its siblings, and deserves a retry rather than a bug report. The wall limit also arms your process's own backstop for a child that is wedged as a whole (see Secure).

jaml's own work on your data, binding a value, reading a result or a thrown value, stops within a few hundred values of a limit or of the context's end, in every sandbox, and never ends a child. In a secure sandbox the child keeps both clocks, and a job still inside a built-in 250 ms after either limit ends the child the way a job stuck after cancel does: that job fails with the limit's cause, naming the piece of code that was running and quoting it as a trusted sandbox does (`$.check: ${…}: exceeded the time limit of 100ms and was still inside a built-in 250ms after it was told to stop`), and the other jobs of the child are replayed. One exception: a job out of wall time inside a built-in while the child's live heap is past three quarters of its ceiling was building something too big, not starved, so it fails with `MemoryLimit` (`exceeded the memory limit of 134217728 bytes, out of wall time too: exceeded the wall time limit of 1s and was still inside a built-in 250ms after it was told to stop`), the way an outside kill under pressure does; a built-in that allocates hundreds of MiB spends its time parked for the collector, on no thread's clock, so the time limit alone would never catch it. In a trusted sandbox the clocks start when the job takes its slot, and a built-in that never returns to the interpreter runs on, as it does past a context's end.

`WithBuildTimeLimit` is the same budget for making an expression and for compiling a `Resolve`'s scripts: a build that computes longer fails with `TimeLimit` (`$: the build exceeded its time limit of 100ms`), located at `$` like a build timeout, and a secure sandbox's child that is still compiling 250 ms later is ended as stuck; a trusted build returns at once while its compile step finishes in the background, as it does past `WithBuildTimeout`, which stays the wall bound on building.

The thread's clock is read with `clock_gettime`. On Linux the time limit is enforced against it. On macOS the clock of another thread cannot be read without cgo, so there a time limit is enforced against the job's wall time, like `WithWallLimit` with the `TimeLimit` cause, and a loaded Mac can blame a script that was only starved; the time a `Resolve` reports (below) is still its thread's own CPU time. Elsewhere CPU time reads as zero and a time limit counts wall time too.

### Meter

```go
ctx = jaml.WithMeter(ctx, func(used jaml.Usage) { log.Printf("cpu %s wall %s", used.CPU, used.Wall) })
```

`WithMeter` reports what a `Resolve` used: `CPU`, the thread's own time, `Wall`, the time from the moment the job started running until it finished or was stopped, measured where the job runs, and `Resident`, the bytes the process running the job held resident when the reading was taken (the child's on a secure sandbox, yours on a trusted one; read from `/proc/self/statm`, so zero off Linux), so the wait for a slot, for a child to start and the transport of the request and the answer never count, nor does compiling its scripts. The sink is called once, before `Resolve` returns, for a job that ran: one that succeeded, one that threw, one that ran out of a limit or was cancelled while it ran, one a secure sandbox's child ended as stuck in a built-in, and one whose child died under it, replays included. For those the child reads the job's thread from outside: just before it exits for memory, and every 250 ms while the job runs, so that a child killed from outside or crashed, including one the kernel refused an allocation to at its address-space ceiling, still leaves a reading at most 250 ms old; a job that died within its first 250 ms reports nothing. On these paths `CPU` is exact on Linux and zero elsewhere. A call refused before it ran, with `InvalidInput`, `Closed` or a build failure, reports nothing; a job replayed after a crash reports its replay only.

### Console

```go
ctx = jaml.WithConsole(ctx, func(line jaml.Line) {
	log.Printf("%v %v: %s", line.Level, line.Path, line.Text)
})
```

Without `WithConsole`, code sees no `console` global, as before. With it, code sees a frozen `console` with `log`, `info`, `warn`, `error` and `debug`, and every line it writes reaches your sink as a `Line`: its `Level` (`Log`, `Info`, `Warn`, `Error` or `Debug`), its `Text`, and its `Path`, the steps from the document's root to the piece of code that wrote it, or to the selected part for a line a script writes. The arguments of one call are joined by spaces: a string as it is, `undefined` and a function as `String()` gives them, anything else as `JSON.stringify` writes it, and `String()` again when that throws, as it does for a cycle or a BigInt. As with the other options, the innermost `WithConsole` wins, and `WithConsole(ctx, nil)` turns the console off. A value named `console` cannot be bound while the console is on.

Lines stream out of a secure sandbox's child as they are written, so they survive whatever stops the `Resolve`: its time limit, a memory kill of the child, or its context's end. Your sink is called in the order the lines were written, from one goroutine per `Resolve`, and every line written before the job ended or was stopped reaches it before `Resolve` returns; a slow sink holds up only its own `Resolve`. A job replayed after a crash writes its lines again, and jaml passes on only those your sink has not seen, so each line arrives once. One `Resolve` keeps at most 1000 lines and 1 MiB of text; a line longer than 8 KiB is cut to fit and ends with `…`, and once a cap is reached the rest are dropped and one last `Warn` line says how many. A trusted sandbox calls your sink the same way, in your process.

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
- Anchors, aliases and merge keys (`<<`) expand. A merge key takes a mapping or a list of mappings; the mapping's own keys win, then earlier mappings in the list win over later ones. Only a plain `<<` is a merge key, and a mapping takes one; a quoted `'<<'` or a `!!str <<` is an ordinary key named `<<`. Errors inside merged or aliased values name the path where the value lands, such as `$.x.a`. Expanded data counts toward the 4096 depth limit, and aliasing is bounded the way `go.yaml.in/yaml/v3` bounds it: once more than 100 aliased values and 1000 values in all have been read, aliased values may make up at most 99% of the values read, falling linearly from 400,000 values to 10% at 4,000,000. On top of that, aliases may expand to at most 1,048,576 values. Excessive aliasing is an `InvalidExpression` error naming the path where it tipped over.
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

goja, the JavaScript engine, cannot limit memory, and it cannot be interrupted inside a built-in call such as a backtracking regular expression or `'x'.repeat(1e9)`. So jaml offers three sandboxes with the same API, made by three constructors. Expressions behave identically in all of them apart from the isolation guarantees and whether the built-ins are frozen.

```go
func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeSecureSandboxWithoutBuiltinsProtection(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)

func WithMemoryLimit(bytes int64) SandboxOption        // secure only; 16 MiB to 1 TiB; default 256 MiB
func WithMaxConcurrency(jobs int) SandboxOption        // at least 1; default GOMAXPROCS
func WithBuildTimeout(timeout time.Duration) SandboxOption // positive; default 5s
func WithBuildTimeLimit(limit time.Duration) SandboxOption // positive; default none; the build's and a Resolve's script compiles' own time
func WithScriptCacheLimit(bytes int64) SandboxOption  // positive; default an eighth of the memory limit, 32 MiB trusted
func WithCallDepthLimit(calls int) SandboxOption       // 1 to 32768; default 4096; counts your calls only
```

| Constructor | Where code runs | Built-ins and values |
|---|---|---|
| `MakeSecureSandbox` | a child process | frozen |
| `MakeSecureSandboxWithoutBuiltinsProtection` | a child process | ordinary |
| `MakeTrustedSandbox` | your process | ordinary |

Protection, frozen built-ins and frozen values, keeps the pieces of code of one `Resolve` apart: none can change what a later one sees. Without it, a piece of code can change a built-in or a value for every piece of code that runs after it in the same `Resolve`, so key order can matter. Your process's safety is the same either way: the child process, its memory ceiling and its isolation do not depend on protection, a value's name stays read-only, and every `Resolve` starts in a fresh runtime, so nothing leaks from one `Resolve` to the next. Pick `MakeSecureSandbox` when the pieces of code of one document, or of several documents resolved together, come from sources that do not trust each other; the freeze costs about 0.65 ms per `Resolve` that runs code, and about 2 ms when it runs scripts. Protection does not keep a script apart from the document: a script runs before the freeze, so it may redefine any built-in (`Array.prototype.map`, `JSON.stringify`, `Object.prototype.admin`) for every piece of code of its `Resolve`. Give a script the trust you give the document itself.

Every sandbox runs at most `WithMaxConcurrency` jobs at once, in arrival order; a job is making an expression or a `Resolve`. `WithBuildTimeout` bounds making itself, not the time a job waits for its slot; the context bounds both. `WithBuildTimeLimit` bounds the time making itself spends computing, the way `WithTimeLimit` does for a `Resolve`, and fails a build that computes past it with `TimeLimit`. A build whose context or timeout ends stops between YAML nodes and pieces of code, and the call returns at once with the cause. A build that finishes as its timeout ends keeps whichever came first, in every sandbox. A build that finishes after its context or its sandbox's context ended still fails with that cause; an expression is never handed back already closed. A bad option is an `InvalidExpression` error.

### Trusted

`MakeTrustedSandbox` runs everything in your process. The context of each call interrupts its JavaScript cooperatively, so an infinite loop ends with its context, and so does binding values or reading a result or a thrown value; but a built-in that never returns to the interpreter runs on, and an allocation bomb takes your process down. A build, or the compile of a `Resolve`'s scripts, that outlives its context or timeout returns to you at once, and keeps its concurrency slot until it has stopped. `WithMemoryLimit` is refused. Its built-ins are ordinary, as in `MakeSecureSandboxWithoutBuiltinsProtection`. Use it for documents you wrote. Untrusted *values* and *arguments* are fine: they are bound as JSON data and never run. Code that hands an untrusted string to `eval`, `new Function` or `new RegExp` runs it as code; trusted mode makes no promise about running untrusted code.

### Secure

`MakeSecureSandbox` and `MakeSecureSandboxWithoutBuiltinsProtection` start one child process that jaml owns and run every expression of the sandbox in it, many at once, each job in its own fresh JavaScript runtime.

- **Building happens in the child.** Your process never interprets document or code text: it hands the text to the child, which parses and compiles it. A refusal comes back with the same kind and message a trusted sandbox gives. Your process keeps the text of each live expression, to build it again in a fresh child when it is next used.
- **Memory.** One ceiling: the child's live heap may grow at most `WithMemoryLimit` over what it held when it started, so heap that your packages build in `init()` does not count. On Linux the child also caps its address space as a backstop that catches a short-lived giant allocation the heap watch could miss, at three times the memory limit plus 256 MiB over what it held at start: it creates its thread pool, one thread per concurrent job, before measuring that, and runs with one glibc malloc arena (`MALLOC_ARENA_MAX=1` in its environment), so the threads of a loaded host neither pass for memory nor lend a body room past the limit. Built expressions are resident and count, and so are compiled scripts, which the child keeps in a cache of at most `WithScriptCacheLimit` (estimated; by default an eighth of `WithMemoryLimit`), dropping the least recently used. The cache and 16 MiB of reserve must fit in the memory limit, or the option is refused. A request is read and decoded while the child watches its heap, and nothing of it is kept once its job ends. A built document resides at about its written size, since an anchored value is read once and shared by its aliases; building it briefly holds the YAML parser's node tree as well, which takes 10 to 25 times the document's bytes, so a 5 MB list of small mappings needs a limit of about 160 MiB to build. The first `Resolve` that reaches a part of a document with no code beneath it encodes the largest such part around it to JSON once and keeps the bytes, with the place of every mapping and list inside them, so every later `Resolve` of that part or of anything inside it sends a slice of those bytes as it is. The kept bytes never exceed the JSON of the document's code-free parts, each encoded once, as a `Resolve` of the whole document would write them (an alias is written out in full at each place it is used); on top of that comes one index entry per mapping and list in them. So a static document without aliases holds one more copy of itself, in JSON, from its first `Resolve` on, however many paths you resolve, and that copy counts against the limit like the document does (in a trusted sandbox it lives in your process). While any job runs, the child checks its heap every millisecond; past the ceiling it collects garbage once, and if the live heap is still over, it exits and the job that caused it fails with `MemoryLimit` ("exceeded the memory limit of N bytes"), which is an `EvaluationFailed`. A doubling loop can reach about twice the limit before it is stopped. On Linux the child also has a hard ceiling the kernel enforces: its address space may grow by at most three times `WithMemoryLimit`, plus 256 MiB, plus 8 MiB per `GOMAXPROCS`, over what it held at its start (`RLIMIT_AS`, which gVisor enforces too). A job that outruns the heap check, such as a string doubled by `+` or a template, `Math.max.apply(null, {length: 2 ** 32 - 1})`, `'x'.repeat(1 << 28)`, or a typed array or `ArrayBuffer` larger than that, has its allocation refused by the kernel; the child exits with the Go runtime's out-of-memory error (or, in a cgo binary, cannot start a thread), and that job fails with `MemoryLimit` after its replay alone, while the jobs that shared its child are replayed and succeed. A 64 MiB child holds at most about 250 MB resident under such a bomb. On macOS and other systems there is no such ceiling: the heap check alone stops a job, so a built-in that allocates a large value in one step, such as `apply` of a long array-like or `repeat`, can briefly hold far more than the limit before the child is stopped. A race-detector build sets no ceiling either, since the race detector's shadow memory grows with the heap. A value counts against the ceiling at about 2.3 times its size as JSON while no code reads it; once read, a text value counts about 4.5 times, and a value of many small objects about 40 times, because each becomes an object of the engine. A child whose allocation the operating system refuses outright is a `MemoryLimit` too. Every `MemoryLimit` reads "exceeded the memory limit of N bytes", however the child died; the child's crash output is never part of it. A child killed from outside jaml, usually by the kernel's out-of-memory killer, is a `MemoryLimit` when its live heap had passed three quarters of the ceiling; otherwise it is a death jaml did not cause: "evaluator died (signal: killed): killed from outside jaml", naming its peak resident memory on Linux, which fails the job with `Unavailable` once its replay alone is killed that way too (see Recovery).
- **Time.** When a call's context ends, the call returns at once. It fails with `EvaluationFailed` matching the context's cause (`context.DeadlineExceeded`, `context.Canceled` or your `context.Cause`) and names the selected path as you asked for it, whether the call was running or still waiting for its slot. The child interrupts the job, and the job keeps its concurrency slot until the child has let go of it. The child is the one that decides a job is stuck: a job still inside a built-in 250 ms after the child is told to stop it makes the child exit ("a job stuck after cancel"), and its other jobs are replayed as below. Only running counts: a job that has finished computing and is still sealing or sending its answer is never stuck, however big the answer or long the queue behind other answers. A build whose context ends is stopped by the child at its next YAML node or piece of code, never by an exit. For a build, only `WithBuildTimeout` can end the child: when one parse or compile step outlives it by 250 ms, the child exits the same way. The same holds for the compile of a `Resolve`'s scripts, which happens before its limits start; to leave it room, a call with scripts and a wall limit allows `WithBuildTimeout` on top of that limit until the child says the scripts compiled, and from then on the wall limit plus 500 ms, before it gives up on a child that sends nothing. As a backstop for a child that is wedged as a whole, your process kills it when it has sent nothing for 5 s after being told to stop a job ("the evaluator stopped answering"), or when it has taken none of a request for 5 s after that call's context ended ("not reading requests"); either way its other jobs are replayed as below. A call whose context is already done fails at once, without touching the child.
- **Recovery.** When the child dies for any reason, every job it was running is a suspect. jaml replays the suspects one at a time, each in a fresh child of its own that holds nothing but that job's expression, with its pinned clock, seed, values, scripts and arguments, while new jobs wait. A suspect that kills the child while running alone fails with its real cause: `MemoryLimit`, or `EvaluatorDied` ("evaluator died" with its exit status and the reason line of its crash, never its goroutine dump). A child killed from outside jaml below the memory pressure line is not the job's fault: when the suspect's own child is killed that way too, the job fails with `Unavailable`, which is worth retrying, rather than `EvaluationFailed`. Every other suspect returns its normal result, as if nothing had happened. Then the sandbox runs jobs concurrently again, in another fresh child. A child that cannot be started fails every job waiting for it, once, with the start error; the next call tries again. A job whose own deadline forced the kill is not replayed; it fails with its context's cause. jaml does not judge: it never flags or refuses an expression, and an expression whose `Resolve` ran out of memory once is tried again on its next `Resolve`. Jobs that only overflowed together all succeed on replay. Replay is safe because jobs are pure: values, scripts and arguments are part of the job, values and arguments are JSON copies, and there is no I/O and no callback into your process. A replay runs the scripts again.
- **Replies.** Each answer travels back to your process as one message of at most a quarter of `WithMemoryLimit` (64 MiB by default), whatever it holds: data, a thrown value or a failure. A bigger one is refused in the child with `EvaluationFailed` ("$.path: the answer is larger than N bytes"). The cap counts the escaped JSON bytes: two for `\n`, `\t`, `\r`, `\b`, `\f`, `\"` and `\\`, six for other control characters, U+2028, U+2029 and invalid UTF-8. Your process reads each message into one buffer of exactly its size and decodes it in place, so it never holds more than that per running job before `Resolve` decodes the data into your target, which builds your Go values on top. A trusted sandbox has no such cap. A child that sends a bigger message anyway is killed, and its jobs see that as its death.
- **Requests.** Each job travels to the child as one message too, capped the same way: a `Resolve` (its values, scripts, arguments and path) or a build (its document) of more than a quarter of `WithMemoryLimit` is refused in your process before anything is sent: a `Resolve` with `InvalidInput` ("the request is larger than N bytes, mostly value "name"", or mostly the values, the arguments or the scripts), a build with `InvalidExpression` ("…, mostly the document"). Your process encodes a request once, straight into the message it sends, and stops as soon as it passes the cap. Values travel only when the selected part holds code or a script is set. Your process checks them either way, so a bad value fails a code-free selection just the same, but values that do not travel count toward neither the cap nor the child's memory. The cap bounds what crosses the wire, not what a value costs once the child binds it: a bound value is charged to the memory ceiling at a measured multiple of its encoded size. A trusted sandbox has no such cap.
- **Clean environment.** The child gets none of your environment variables. It gets exactly `JAML_EVALUATOR`, `JAML_MEMORY_LIMIT`, `JAML_BUILTINS` (`frozen` or `mutable`), `JAML_CALL_DEPTH`, `JAML_NONCE` (per start), `JAML_PARENT` (your process id), `ZONEINFO` when you have it set, and `TZ` when your local zone can be forwarded. A package whose `init()` needs another variable fails in the child.
- **Local time.** The child uses your local zone when it can find it the way you did: a zone from `TZ` at startup, by name or by the path of its file, or one you set with `time.Local, _ = time.LoadLocation(name)`, including one found through `ZONEINFO`. Then trusted and secure sandboxes agree on local `Date` methods. A zone without such a name (`time.FixedZone`, a renamed or hand-built location) cannot be forwarded, so the child uses the system zone. The zone is read when a child starts.
- **Signals and parents.** The child runs in its own process group, so a terminal Ctrl-C or a signal sent to your process group does not reach it. It exits when your process dies: at once when idle, within about a millisecond when busy. An idle child uses no CPU.
- **Cost.** Starting a child and answering its first `Resolve` takes about 6 ms without protection and about 8 ms with it. After that, a `Resolve` that runs code costs about 0.06 ms without protection and about 0.7 ms with it, the freeze included. A start, first or after a death, is bounded by the sandbox's context and by a fixed 30 s, sized for a loaded machine; never by `WithBuildTimeout`, which bounds builds only, and never by the call that needed it, which stops waiting when its own context ends. Recovery costs one start per suspect. `Health()` tells how the sandbox is doing: `Resident`, the last resident-memory reading its child reported (every usage report carries one, so it is at most a job ago; a trusted sandbox reports your own process), `AddressCeiling`, the ceiling the child set on its address space when it started (Linux only, see Memory), and `Restarts`, how many children were started after the first, whether for a death or a retirement. A `Resolve` is one request to the child, which sends back the data, decoded into your target in your process. Each distinct script is compiled once, and one that fails to compile fails again without a second compile: both are kept in each sandbox's cache, bounded by its estimated size, least recently used first out: `WithScriptCacheLimit`, by default an eighth of `WithMemoryLimit` in a secure sandbox's child and 32 MiB of your process's heap in a trusted sandbox. A compiled script is estimated at about 16 times its source, so the default cache of a 128 MiB child holds about 16 scripts of 60 KB. Size it to the scripts you cycle through: 40 distinct 60 KB scripts resolved in turn cost about 8 ms per `Resolve` with the default cache, which never hits, and 0.2 ms trusted, 0.4 ms without protection and 2.4 ms with it once a 96 MiB cache holds them all. The cache is memory the child keeps whether or not you resolve again.

The child is your own binary, started again with a marker in its environment; on Linux it is the running image (`/proc/self/exe`), even if the file has been replaced since. jaml's `init()` sees the marker, identifies itself to the parent with a greeting that carries the protocol version and the nonce, serves over file descriptors 3, 4 and 5, and exits, so your `main()` never runs in the child. The `init()` functions of packages that Go initializes before jaml do run there. A child that answers with something other than a jaml greeting, because the executable is not a jaml binary, is `Unsupported`. Every other way a child can fail to start is `Unavailable`, located at `$` from a constructor and at the selected path when a respawn fails under a call, with a message naming why: it could not be executed, it took longer than 30 s to greet (or the context passed to its constructor ended first), it speaks another protocol version because the executable was replaced since your process started, or it died before it greeted, such as one whose `init()` panics, naming its exit status and the reason line of its crash. The error wraps its cause, so a start that ran out of time matches `context.DeadlineExceeded`. This holds for the first start in either secure constructor and for every respawn after a child dies or retires: a respawn that fails refuses every call waiting for it with the same `Unavailable` error, and the next call tries again. A trusted sandbox starts nothing and never returns `Unavailable`. Secure sandboxes need a Unix that can start the binary again: they are not supported on Windows, iOS, js or wasip1 (`Unsupported`), or when jaml is built into a C shared library or a plugin.

### Limits

Every sandbox shares these limits:

- JavaScript calls nest at most 4096 deep, or as deep as `WithCallDepthLimit` allows, up to 32,768; deeper recursion fails with `CallDepthLimit`, an `EvaluationFailed` naming the limit ("exceeded the maximum call depth of 4096"). The limit counts the calls of your code only, the same in every sandbox: protection adds no call of its own, so `WithCallDepthLimit(1)` still runs `${ 1 }`. The one exception is reading a script's top-level declaration from a piece of code, which goes through one call jaml makes for you and counts as one. Calls that recurse through a built-in, such as a function calling itself from a `map` callback, also nest on the Go stack, and unwinding an overflow of them takes time that grows with the square of the limit: about 0.15 s at 4096 and 9 s at 32,768. That unwinding is inside the engine, so it does not stop for the context or `WithTimeLimit`: a secure sandbox ends it the way it ends any job stuck in a built-in, 250 ms after the job is told to stop, while a trusted sandbox's call returns only once the unwinding is done.
- A value that holds code (value code, or text with `${{`) and a script are each at most 65,536 bytes as written; a longer one is refused with `InvalidExpression`, naming its path or the script.
- Code is also bounded in shape, because goja parses and compiles recursively. Before any parse, one pass over the code refuses, with `InvalidExpression` naming the path or the script and the line and column ("code too deep at line 1, column 1538: arrow functions nest more than 512 deep"): brackets, braces, parentheses, template substitutions and classes nested more than 512 deep; arrow functions nested more than 512 deep; arrow parameter lists nested more than 4 deep, one inside another's parentheses through any brackets, defaults, patterns or `async` between them (`(a = (b = (c = (d = (e) => 0) => 0) => 0) => 0) => 0`), because goja parses each such list twice, so each level would double the parse; statements (`if`, loops, labels, `else if` chains) nested more than 512 deep; more than 512 prefix operators in a row (`!`, `-`, `typeof`, `new`, …); more than 512 assignments, conditionals and `**` chained; more than 4096 binary operators chained; more than 2048 member accesses, calls and tagged templates chained. Code within these bounds compiles in a few MiB of stack, so building needs nothing beyond Go's default stack limits. The same pass finds where `${ }` and `${{ }}` end, in one reading of the text.
- Within these bounds no shape makes goja repeat its work level after level, but compile time still grows faster than the length of the code for some shapes: the slowest measured, 4096 chained binary operators, compiles in about 0.12 s, and 64 KiB inside four nested arrow parameter lists in about 60 ms (on an Apple M-series laptop). A script is parsed once. `WithBuildTimeout` bounds building: a trusted build returns at its timeout while the compile step finishes in the background, holding its concurrency slot; a secure build's child is killed 250 ms after its timeout, and its other jobs are replayed.
- Values, arguments, results and documents nest at most 4096 deep. Deeper data is refused, naming the value or the path where it crosses the limit: a document with `InvalidExpression`, a value or an argument with `InvalidInput`, a result with `EvaluationFailed`.
- `JSON.parse` refuses text nested deeper than 4096 with a `RangeError`, so a deep string value cannot be turned into deep data.
- Data code builds for itself is not bounded. Some built-ins (`JSON.stringify`, `String` or `join` of an array, `flat`) walk it recursively on the Go stack, and a value nested about a million deep overflows that stack, which Go cannot recover from. It is the document's own risk, like an allocation bomb: a trusted sandbox takes your process down; a secure sandbox's child dies, and that `Resolve` fails with the cause once it is replayed alone.
- A Go panic inside the engine, such as goja's `'x'.repeat(2 ** 52)`, fails the `Resolve` with `EvaluationFailed` instead of reaching your code.
- A Go panic while building, such as goja's parser on `'\u{10FFFF}'`, refuses the expression with `InvalidExpression`, naming the value's path (`$` for a single expression); neither the host nor a secure sandbox's child goes down.
- A result may hold at most 1,048,576 values, each list and object counting as one. A bigger result is refused as a whole (`result holds more than 1048576 values`), before jaml allocates room for it, so a sparse list cannot claim more than it holds.
- The runtime has no I/O, no `require`, no timers, and source-map comments are not followed.

## API

```go
func MakeSecureSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeSecureSandboxWithoutBuiltinsProtection(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func MakeTrustedSandbox(ctx context.Context, options ...SandboxOption) (*Sandbox, error)
func (s *Sandbox) MakeExpressionFromJaml(ctx context.Context, jaml string, options ...Option) (*Expression, error)
func (s *Sandbox) MakeExpression(ctx context.Context, expression string, options ...Option) (*Expression, error)
func (s *Sandbox) Health() Health

func (e *Expression) At(path ...string) *Expression
func (e *Expression) Copy(ctx context.Context) (*Expression, error)
func Resolve[T any](ctx context.Context, e *Expression, args ...any) (T, error)

func WithValues(ctx context.Context, values map[string]any) context.Context
func WithScript(ctx context.Context, source string) context.Context
func WithFixedClock(ctx context.Context, time time.Time) context.Context
func WithRandomSeed(ctx context.Context, seed uint64) context.Context
func WithTimeLimit(ctx context.Context, limit time.Duration) context.Context
func WithWallLimit(ctx context.Context, limit time.Duration) context.Context
func WithMeter(ctx context.Context, sink func(Usage)) context.Context
func WithConsole(ctx context.Context, sink func(Line)) context.Context

func WithNullCoercion(coercion Coercion) Option      // Loose (default) or Strict
func WithUndefinedCoercion(coercion Coercion) Option // Loose or Strict (default)
func AsFunction() Option                             // MakeExpression only: refuse anything but a function literal
func WithSigil(sigil string) Option                  // MakeExpressionFromJaml only: what opens code; default "$"
func WithMemoryLimit(bytes int64) SandboxOption
func WithMaxConcurrency(jobs int) SandboxOption
func WithBuildTimeout(timeout time.Duration) SandboxOption
func WithBuildTimeLimit(limit time.Duration) SandboxOption
func WithScriptCacheLimit(bytes int64) SandboxOption
func WithCallDepthLimit(calls int) SandboxOption

type Thrown struct {
	Message string
	Value   any
}

type Failure struct {
	Path   string
	Reason string
}

type Line struct {
	Path  []string
	Level Level // Log, Info, Warn, Error or Debug
	Text  string
}

type Usage struct {
	CPU      time.Duration // the thread's own time
	Wall     time.Duration // from the job's start to its end, where it ran
	Resident int64         // bytes resident in the process that ran the job, when read; zero off Linux
}

type Health struct {
	Resident       int64 // the child's last reported resident bytes (a trusted sandbox's own); zero off Linux
	AddressCeiling int64 // the child's address-space ceiling in bytes; zero without one
	Restarts       int   // children started after the first
}
```

### Errors

```go
var (
	ExpressionError   // base sentinel for every jaml error
	InvalidExpression // bad YAML, bad JavaScript syntax, a bad script, bad options, a secure sandbox's build request past its cap
	InvalidTarget     // T holds, anywhere inside, a type encoding/json cannot decode into (a func, a chan, a complex number, a non-empty interface, a map whose keys are not text or integers) or a jaml.Expression by value; decided before any code runs
	MissingPath       // the path names nothing in the document
	EvaluationFailed  // code or a script threw or produced something that is not data; a call was cancelled; an answer passed its cap; a secure sandbox's child ran out of memory or died
	CoercionFailed    // json.Unmarshal refused the data, or strict null/undefined refused a result
	Closed            // the expression's own context or its sandbox's has ended; the message says which
	Unsupported       // a secure sandbox on a platform that cannot run one, or a child that is not a jaml evaluator
	Unavailable       // a secure sandbox's evaluator could not be started, first or after a respawn (exec failure, start deadline, death before greeting, protocol version mismatch), or was killed from outside jaml while it replayed a job alone
	InvalidInput      // Resolve refused what the call brought, before running it: a value or argument that is not JSON data, nests deeper than 4096, holds text that is not valid UTF-8 or a number beyond a 64-bit float (`1e400`), a value named like a JavaScript global or not in valid UTF-8, a time limit or a wall time limit that is not positive, or a secure sandbox's request past its cap
	MemoryLimit       // derives from EvaluationFailed: a secure sandbox's job ran out of memory
	TimeLimit         // derives from EvaluationFailed: a Resolve computed longer than WithTimeLimit allows, or a build or a script compile longer than WithBuildTimeLimit
	WallLimit         // derives from EvaluationFailed: a Resolve ran longer in wall time than WithWallLimit allows
	CallDepthLimit    // derives from EvaluationFailed: JavaScript calls nested deeper than WithCallDepthLimit allows (4096 by default)
	EvaluatorDied     // derives from EvaluationFailed: a secure sandbox's child crashed, not for memory and not killed from outside, again when its job was replayed alone
)
```

All derive from `ExpressionError`, so `errors.Is(err, jaml.ExpressionError)` matches any of them, and every sandbox returns the same kind and the same message for the same failure. An error also wraps its cause: a cancelled call matches `context.DeadlineExceeded` (or `context.Canceled`, or your `context.Cause`), a failed decode reaches `encoding/json`'s error, and a throw reaches `*jaml.Thrown`. Every message leads with a location: `$` for the root, `$.a.b` deeper. An error about no particular place, such as a bad option or a cancelled build, is located at `$`. So is a value, argument or script that a call brings and jaml refuses before running anything. A failure of a running call that is not about one piece of code, such as running its scripts, its time limit while it binds values or runs scripts, its cancel, or a child that died under it, is located at the path you selected. For example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: expression error
$.config: its own context, the one it was made or copied with, has ended: closed: expression error
```

Every jaml error also carries its parts as a `*jaml.Failure`, so you can show the reason without cutting up the message:

```go
var failure *jaml.Failure
if errors.As(err, &failure) {
	_ = failure.Path   // "$.config.servers[0].port", or "$" for the root
	_ = failure.Reason // "ReferenceError: port is not defined"
}
```

`Path` is the location the message leads with. `Reason` is the rest of the message without the location, the label and the kind; the label is the quoted code, or `script 1`, `binding values` or `binding arguments` for what a call brought along: `result is undefined, which strict undefined coercion refuses`, `syntax error at line 2, column 16 of the expression: Unexpected number`, `not a function literal: write an arrow function or a function expression`. For code that threw, a script included, `Reason` is `Thrown.Message`, and the `*jaml.Thrown` stays reachable too. `failure.Error()` prints exactly what `err.Error()` prints.

A `Closed` error names which context ended: the expression's own (the one passed to `MakeExpression…` or `Copy`), or the sandbox's. A strict undefined result from an arrow whose body is a block adds a hint, since `() => { ok: true }` is a block with a label, not an object: write `() => ({ ok: true })`.

A key longer than 60 characters appears in a location as its first 60 followed by `…`. Messages stay on one line and stay short: quoted code is collapsed to single spaces and cut at 60 characters, a thrown message at 200, and a syntax error gives its line and column in characters and quotes the code from that point. A getter that throws while jaml reads a result names where it was: `reading result.a.b: Error: x`.

## License

See [LICENSE](LICENSE).
