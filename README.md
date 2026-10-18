# jaml

YAML templates whose values can hold JavaScript expressions, evaluated against your variables when you decode. jaml parses the YAML into plain data once, walks it at `Eval` time replacing every expression with its result, then hands the data to `encoding/json` to decode into your type.

## Install

```sh
go get github.com/gleno/jaml
```

## What it gives you

- **Two expression forms.** `${ … }` as a whole value is a typed result (a number stays a number, a list stays a list). `${{ … }}` inside text interpolates.
- **Nothing runs until the data arrives.** `MakeTemplate` parses the YAML and compiles every expression, so syntax errors surface immediately, but evaluates nothing.
- **Results are data.** An expression must produce JSON data: scalars, lists, objects. Functions, symbols, cycles, `NaN`, `Date`s and class instances are errors naming the path to the offending value.
- **Decoding is `encoding/json`.** Struct tags, embedded structs, `json.Unmarshaler`, `json.RawMessage`: whatever `json.Unmarshal` does, jaml does.
- **Null and undefined on your terms.** `Loose` or `Strict` for expression results, configured separately.
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
- **`.inf` and `.nan` are refused**, since JSON cannot carry them.
- **Plain dates are timestamps.** `2026-10-18` decodes into a `time.Time`, and into a `string` as `"2026-10-18T00:00:00Z"`. Quote it to keep the spelling.
- **Integers beyond int64 lose digits.** Quote them and decode into a string.
- `0644` is octal (420). Write `0o644`, `644` or `"0644"` to say what you mean.
- Anchors, aliases and merge keys (`<<`) expand. Tags are applied by the parser and then forgotten: `!!str 12` is the text `"12"`.
- Only the first document is read.

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
	InvalidTarget    // into is not a non-nil pointer
	MissingPath      // the path names nothing in the template
	EvaluationFailed // an expression threw, ran out of budget, or produced something that is not data
	CoercionFailed   // json.Unmarshal refused the data, or strict null/undefined refused a result
)
```

All derive from `TemplateError`, so `errors.Is(err, jaml.TemplateError)` matches any of them. Messages lead with the location, for example:

```
$.config.servers[0].port: ${port}: ReferenceError: port is not defined: evaluation failed: template error
```

## License

See [LICENSE](LICENSE).
