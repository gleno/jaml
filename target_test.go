package jaml

import (
	"testing"
	"time"
)

func TestTheTargetMustBeANonNilPointer(t *testing.T) {
	template := mustTemplate(t, "a: 1\n")
	var typedNil *map[string]int
	cases := map[string]struct {
		into     any
		fragment string
	}{
		"nil":         {nil, "got nil"},
		"value":       {map[string]int{}, "got map[string]int"},
		"typed nil":   {typedNil, "got nil *map[string]int"},
		"not pointer": {3, "got int"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			expectError(t, template.Eval(c.into, nil), InvalidTarget, "non-nil pointer", c.fragment)
		})
	}
}

func TestUnsupportedTargetTypesAreRefused(t *testing.T) {
	cases := map[string]func(*Template) error{
		"chan":         func(template *Template) error { var out chan int; return template.Eval(&out, nil, "v") },
		"func":         func(template *Template) error { var out func(); return template.Eval(&out, nil, "v") },
		"complex":      func(template *Template) error { var out complex128; return template.Eval(&out, nil, "v") },
		"int map keys": func(template *Template) error { var out map[int]string; return template.Eval(&out, nil, "m") },
		"interface":    func(template *Template) error { var out error; return template.Eval(&out, nil, "v") },
	}
	template := mustTemplate(t, "v: 1\nm: {1: a}\n")
	for name, eval := range cases {
		t.Run(name, func(t *testing.T) {
			expectError(t, eval(template), InvalidTarget, "is not a supported target type")
		})
	}
}

func TestFieldsMissingFromTheTemplateKeepTheirValues(t *testing.T) {
	template := mustTemplate(t, "host: example.com\n")
	out := struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}{Host: "localhost", Port: 8080}
	if err := template.Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out.Host, "example.com")
	expectEqual(t, out.Port, 8080)
}

func TestMapsMergeIntoExistingMaps(t *testing.T) {
	template := mustTemplate(t, "b: 2\n")
	out := map[string]int{"a": 1}
	if err := template.Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, map[string]int{"a": 1, "b": 2})
}

func TestSlicesAreReplaced(t *testing.T) {
	template := mustTemplate(t, "[3]\n")
	out := []int{1, 2}
	if err := template.Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, []int{3})
}

type Embedded struct {
	Shared string `json:"shared"`
	Inner  int    `json:"inner"`
}

type embeddedPointer struct {
	Deep string `json:"deep"`
}

type EmbeddedPointer = embeddedPointer

func TestStructFieldsFollowJSONNaming(t *testing.T) {
	type shape struct {
		Embedded
		*EmbeddedPointer
		Named    string `json:"renamed"`
		Untagged string
		Skipped  string `json:"-"`
		Dash     string `json:"-,"`
		Shared   string `json:"shared"`
		hidden   string
	}
	template := mustTemplate(t, "renamed: a\nUntagged: b\n\"-\": c\nshared: outer\ninner: 1\ndeep: d\n")
	got := evalAs[shape](t, template, nil)
	expectEqual(t, got.Named, "a")
	expectEqual(t, got.Untagged, "b")
	expectEqual(t, got.Dash, "c")
	expectEqual(t, got.Shared, "outer")
	expectEqual(t, got.Embedded, Embedded{Inner: 1})
	expectEqual(t, got.EmbeddedPointer.Deep, "d")
	expectEqual(t, got.hidden, "")

	var out shape
	expectError(t, mustTemplate(t, "Skipped: x\n").Eval(&out, nil), CoercionFailed, `has no field "Skipped"`)
	expectError(t, mustTemplate(t, "hidden: x\n").Eval(&out, nil), CoercionFailed, `has no field "hidden"`)
	expectError(t, mustTemplate(t, "Named: x\n").Eval(&out, nil), CoercionFailed, `has no field "Named"`)
}

func TestNestedTargetsDecodeTogether(t *testing.T) {
	type limits struct {
		Timeout time.Duration `json:"timeout"`
		Retries *int          `json:"retries"`
	}
	type service struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
		Limits limits            `json:"limits"`
		Extra  any               `json:"extra"`
	}
	template := mustTemplate(t, `
services:
  - name: ${{prefix}}-api
    labels: {team: "${{team}}", tier: 1}
    limits: {timeout: 30s, retries: "${ retries }"}
    extra: "${ [1, {two: 2}] }"
`)
	got := evalAs[[]service](t, template, map[string]any{"prefix": "app", "team": "core", "retries": 3}, "services")
	retries := 3
	expectEqual(t, got, []service{{
		Name:   "app-api",
		Labels: map[string]string{"team": "core", "tier": "1"},
		Limits: limits{Timeout: 30 * time.Second, Retries: &retries},
		Extra:  []any{int64(1), map[string]any{"two": int64(2)}},
	}})
}

func TestOptionsAreValidated(t *testing.T) {
	cases := map[string]struct {
		option   Option
		fragment string
	}{
		"zero null":      {WithNullCoercion(Coercion(0)), "null coercion must be jaml.Loose or jaml.Strict, got 0"},
		"bad undefined":  {WithUndefinedCoercion(Coercion(7)), "undefined coercion must be"},
		"zero timeout":   {WithTimeout(0), "timeout must be positive"},
		"negative timer": {WithTimeout(-time.Second), "timeout must be positive"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := MakeTemplate("a: 1\n", c.option)
			expectError(t, err, InvalidTemplate, c.fragment)
		})
	}
}

func TestTheLastOptionWins(t *testing.T) {
	template := mustTemplate(t, "v: ~\n", WithNullCoercion(Strict), WithNullCoercion(Loose))
	expectEqual(t, evalAs[string](t, template, nil, "v"), "")
}
