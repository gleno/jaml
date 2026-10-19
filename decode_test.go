package jaml

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTargetsDecodeThroughEncodingJSON(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type limits struct {
			Retries *int `json:"retries"`
		}
		type service struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			Limits limits            `json:"limits"`
			Extra  json.RawMessage   `json:"extra"`
			Hidden string            `json:"-"`
		}
		template := mustTemplate(t, sb, `
services:
  - name: ${{prefix}}-api
    labels: {team: "${{team}}"}
    limits: {retries: "${ retries }"}
    extra: "${ [1, {two: 2}] }"
    Hidden: nope
`)
		got := evalAs[[]service](t, template, map[string]any{"prefix": "atlas", "team": "rev", "retries": 3}, "services")
		three := 3
		expectEqual(t, len(got), 1)
		expectEqual(t, got[0].Name, "atlas-api")
		expectEqual(t, got[0].Labels, map[string]string{"team": "rev"})
		expectEqual(t, got[0].Limits, limits{Retries: &three})
		expectEqual(t, string(got[0].Extra), `[1,{"two":2}]`)
		expectEqual(t, got[0].Hidden, "")
	})
}

func TestMismatchedTypesAreCoercionErrors(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type shape struct {
			Port int `json:"port"`
		}
		var out shape
		expectError(t, mustTemplate(t, sb, "port: ${'eighty'}\n").Eval(t.Context(), &out, nil), CoercionFailed, "$.port: json: cannot unmarshal string into Go struct field shape.port")
		var text string
		expectError(t, mustTemplate(t, sb, "v: ${1}\n").Eval(t.Context(), &text, nil, "v"), CoercionFailed, "$.v")
	})
}

type selfEmbedding struct {
	*SelfEmbedding
	V int `json:"v"`
}

type SelfEmbedding = selfEmbedding

func TestRecursiveEmbeddingTerminates(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		got := evalAs[SelfEmbedding](t, mustTemplate(t, sb, "v: 1\n"), nil)
		expectEqual(t, got.V, 1)
	})
}

func TestTargetMustBeANonNilPointer(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, "a: 1\n")
		var out map[string]any
		expectError(t, template.Eval(t.Context(), out, nil), InvalidTarget, "map[string]interface {}")
		expectError(t, template.Eval(t.Context(), nil, nil), InvalidTarget, "nil")
		expectError(t, template.Eval(t.Context(), (*int)(nil), nil), InvalidTarget, "nil *int")
	})
}

type wrappingPort struct{ N int }

func (p *wrappingPort) UnmarshalJSON(written []byte) error {
	var raw struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(written, &raw); err != nil {
		return fmt.Errorf("port: %w", err)
	}
	p.N = raw.Number
	return nil
}

type delegatingPort struct{ N int }

func (p *delegatingPort) UnmarshalJSON(written []byte) error {
	var raw struct {
		Number int `json:"number"`
	}
	return json.Unmarshal(written, &raw)
}

type portsConfig[P any] struct {
	Name    string `json:"name"`
	Servers []struct {
		Host string `json:"host"`
		Port P      `json:"port"`
	} `json:"servers"`
}

const portsTemplate = `
config:
  name: a-rather-long-name-value
  servers:
    - host: a
      port: {number: 1}
    - host: b
      port: {number: "two"}
`

func TestAnUnmarshalerOffsetIsNeverMappedOntoTheDocument(t *testing.T) {
	cases := map[string]func() any{
		"wrapping":   func() any { return new(portsConfig[wrappingPort]) },
		"delegating": func() any { return new(portsConfig[delegatingPort]) },
	}
	for name, newOut := range cases {
		t.Run(name, func(t *testing.T) {
			both := agreement{text: portsTemplate, path: []string{"config"}}.eval(t, newOut)
			both.expectRefused(t, CoercionFailed, "$.config: ", "json: cannot unmarshal string")
			for _, err := range []error{both.trusted, both.isolated} {
				if strings.Contains(err.Error(), "$.config.") {
					t.Fatalf("blamed a path inside the selection: %v", err)
				}
				var mismatch *json.UnmarshalTypeError
				if !errors.As(err, &mismatch) {
					t.Fatalf("expected *json.UnmarshalTypeError in %v", err)
				}
			}
		})
	}
}

type EmbeddedPort struct {
	Port int `json:"port"`
}

type embeddingAndMap struct {
	EmbeddedPort
	Limits map[string]int `json:"limits"`
}

func TestBlameStillReachesEmbeddedFieldsAndMapValues(t *testing.T) {
	agreement{text: "port: x\nlimits: {cpu: 1}\n"}.eval(t, func() any { return new(embeddingAndMap) }).expectRefused(t, CoercionFailed, "$.port: json: cannot unmarshal string")
	agreement{text: "port: 1\nlimits: {cpu: 1, mem: x}\n"}.eval(t, func() any { return new(embeddingAndMap) }).expectRefused(t, CoercionFailed, "$.limits.mem: json: cannot unmarshal string")
}
