# jaml

YAML templates whose values can hold JavaScript expressions, evaluated against your variables when you decode. Parse once, check the YAML and the JavaScript up front, then decode into Go types as often as you like, with every value checked against its target and every error naming the YAML path it came from.

## Install

```sh
go get github.com/gleno/jaml
```

## What it gives you

- **Two expression forms.** `${ … }` as a whole value is a typed result (a number stays a number, a list stays a list). `${{ … }}` inside text interpolates.
- **Nothing runs until the data arrives.** `MakeTemplate` parses the YAML and compiles every expression, so syntax errors surface immediately, but evaluates nothing. `Eval` runs the expressions it needs, against the vars you pass.
- **Results are data.** An expression must produce YAML data: scalars, lists, objects. Functions, symbols, cycles, `NaN`, `Date`s and class instances are errors naming the path to the offending value.
- **Checked coercion.** Every value is checked against its Go target. Errors name the YAML path, line and column, and both types.
- **Null and undefined on your terms.** `Loose` turns them into zero values, `Strict` refuses them, configured separately.
- **Fewer YAML gotchas.** YAML 1.2 scalars (`no` is text, not `false`), no silent octal, duplicate keys refused, text targets keep the spelling you wrote.
- **Bounded evaluation.** A time budget per `Eval`, a call-depth limit, and a cap on result size.
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

template, err := jaml.MakeTemplate(text,
	jaml.WithNullCoercion(jaml.Loose),
	jaml.WithUndefinedCoercion(jaml.Strict))
if err != nil {
	return err
}

var cfg Config
err = template.Eval(&cfg, map[string]any{"name": "abc"}, "config")
// cfg == Config{Value1: 123, Value2: 2, Value3: "some string abc"}
```

`Eval(into, vars, path...)` decodes into the non-nil pointer `into`. The optional `path` selects a subtree first, one key or list index per element (`"config"`, `"servers", "0"`); only that subtree is evaluated. A path that does not exist is a `MissingPath` error.

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
- To write the delimiters literally, use a JavaScript string: `${{'${{'}}` or `${'${x}'}`. Or tag the value `!!str`, which keeps it verbatim with no expressions at all.
- **Keys are never evaluated.**
- Expressions run in strict mode. The vars are globals, deep-copied from `vars` the way `encoding/json` marshals them, so an expression cannot change your data. A var name that is not a JavaScript identifier is still reachable as `globalThis["my-var"]`. A missing var is a `ReferenceError`; a missing property of a var is `undefined`.
- All expressions in one `Eval` share one runtime, so one expression can see globals another one set. Separate `Eval` calls never share anything.

### YAML gotchas

- **Quote expressions that contain `: `, `#`, or start a flow collection.** `limits: ${ {cpu: 2} }` is a YAML error, because `: ` inside a plain scalar starts a mapping. Write `limits: "${ {cpu: 2} }"`. Inside flow collections (`[…]`, `{…}`) every expression needs quotes. The same goes for `?:` ternaries.
- **YAML 1.2 core scalars.** Only `true`/`false` (any case style YAML allows) are bools; `yes`, `no`, `on`, `off`, `y`, `n` are text. `1_000` and `0b101` are text. `0o17` is octal and `0x1F` is hex.
- **No silent octal.** `0644` means 420 in YAML 1.1 and 644 in YAML 1.2. jaml refuses to guess: it is an error into any number or `any` target, and stays `"0644"` into a text target. Write `0o644`, `644` or `"0644"`.
- `.inf` and `.nan` are not JSON data. They decode only into text targets.
- Duplicate keys, multiple documents, merge keys (`<<`), and tags other than `!!str`, `!!int`, `!!float`, `!!bool`, `!!null`, `!!map`, `!!seq` are errors.
- Anchors and aliases work, but a template whose aliases expand past 1,048,576 nodes is refused.

## Coercion

Struct fields are matched by their `json` tag name, or by the Go field name when untagged, exactly (no case folding). Fields of embedded structs are promoted, and a shallower field wins over a deeper one. `json:"-"` and unexported fields are skipped. A key with no matching field is an error. Fields the template does not mention keep the value `into` already had, so you can preset defaults.

| Target | Accepts |
| --- | --- |
| `string` | **Everything.** Text as is. YAML scalars keep the spelling written (`1.10`, `0x1F`, `TRUE`). JavaScript numbers as JavaScript prints them (`1e+21`). Lists and objects as compact JSON, keys in document order. |
| `bool` | Bools, and the exact text `"true"` or `"false"`. |
| `int*`, `uint*` | Numbers with no fractional part that fit the type (`2.0` and `1e3` are fine, `1.5` is not). Text that is a JSON number (`"42"`, but not `" 42"`, `"0x10"` or `"1_000"`). Unsigned targets refuse negatives. |
| `float*` | Any finite number, and text that is a JSON number. `float32` refuses values out of its range. |
| `time.Duration` | Text that `time.ParseDuration` reads (`"1m30s"`). Bare numbers are refused, since their unit would be a guess. |
| `encoding.TextUnmarshaler` | Scalars, as text (for example `time.Time`, `netip.Addr`). |
| `json.Unmarshaler` | Anything, handed over as JSON. |
| struct | Objects. |
| `map[K]V`, `K` a string kind | Objects. Entries merge into an existing map. |
| slice | Lists. An existing slice is replaced. |
| array | Lists of exactly the array's length. |
| pointer | Whatever its element accepts; allocated when nil. |
| `any` | Anything, as `bool`, `string`, `int64` (whole numbers that fit) or `float64`, `[]any`, `map[string]any`. |

Everything else (channels, functions, complex numbers, non-empty interfaces, maps with non-string keys) is an `InvalidTarget` error. Numbers never coerce to bools or bools to numbers, and scalars never become lists or objects.

On an error, `into` may be partly written.

## Null and undefined

YAML `~`, `null` and empty values are null. JavaScript can produce both `null` and `undefined`. Each has its own setting:

| | `Loose` | `Strict` |
| --- | --- | --- |
| null | the target's zero value | the zero value (nil) for pointers, `any`, maps and slices; an error for everything else |
| undefined | the target's zero value | always an error |

Strict null refuses null only where Go cannot represent it. Strict undefined refuses it everywhere, because Go has no undefined: turning it into nil would hide a mistyped property.

**Defaults:** null is `Loose` (an empty YAML value usually means "unset"), undefined is `Strict` (it usually means a typo).

The same rules apply inside lists, objects and interpolation:

- `${{ }}` of null or undefined interpolates `""` when loose, and fails when strict.
- When a list or object becomes JSON text, null is written as `null`. Undefined is written as `null` when loose, and fails when strict.
- Loose null or undefined that lands on a field overwrites what was there with the zero value.

## Safety

Expressions are meant for configuration you mostly trust, but they cannot hang or crash your process:

- Each `Eval` has a time budget, one second by default, shared by all its expressions. Change it with `WithTimeout`. Once it runs out, the running expression and every one after it fail.
- Calls nest at most 4096 deep.
- A result may hold at most 1,048,576 values.
- The runtime has no I/O, no `require`, no timers, and source-map comments are not followed.

Memory is not bounded: a single built-in call such as `'x'.repeat(1e9)` can allocate before the budget can interrupt it. For hostile input, run in a process with a memory limit.

## API

```go
// Parse and compile a template. Nothing is evaluated.
func MakeTemplate(text string, options ...Option) (*Template, error)

// Evaluate the subtree at path and decode it into the non-nil pointer into.
func (t *Template) Eval(into any, vars map[string]any, path ...string) error

// Options. Coercion is Loose or Strict.
func WithNullCoercion(coercion Coercion) Option      // default Loose
func WithUndefinedCoercion(coercion Coercion) Option // default Strict
func WithTimeout(timeout time.Duration) Option       // default 1s per Eval
```

### Errors

```go
var (
	TemplateError    // base sentinel for every jaml error
	InvalidTemplate  // bad YAML, bad JavaScript syntax, bad options
	InvalidTarget    // into is not a non-nil pointer, or a target type jaml cannot fill
	MissingPath      // the path names nothing in the template
	EvaluationFailed // an expression threw, ran out of budget, or produced something that is not data
	CoercionFailed   // a value does not fit its target, or strict null/undefined refused it
)
```

All derive from `TemplateError`, so `errors.Is(err, jaml.TemplateError)` matches any of them. Messages lead with the location, for example:

```
$.config.servers[0].port (line 3, column 13): cannot coerce string "eighty" into int: coercion failed: template error
```

## License

See [LICENSE](LICENSE).
