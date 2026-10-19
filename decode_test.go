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
		type _Limits struct {
			Retries *int `json:"retries"`
		}
		type _Service struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
			Limits _Limits           `json:"limits"`
			Extra  json.RawMessage   `json:"extra"`
			Hidden string            `json:"-"`
		}
		var parsed = mustJaml(t, sb, `
services:
  - name: ${{prefix}}-api
    labels: {team: "${{team}}"}
    limits: {retries: "${ retries }"}
    extra: "${ [1, {two: 2}] }"
    Hidden: nope
`)
		var got = evalAs[[]_Service](t, parsed, map[string]any{"prefix": "atlas", "team": "rev", "retries": 3}, "services")
		var three = 3
		expectEqual(t, len(got), 1)
		expectEqual(t, got[0].Name, "atlas-api")
		expectEqual(t, got[0].Labels, map[string]string{"team": "rev"})
		expectEqual(t, got[0].Limits, _Limits{Retries: &three})
		expectEqual(t, string(got[0].Extra), `[1,{"two":2}]`)
		expectEqual(t, got[0].Hidden, "")
	})
}

func TestMismatchedTypesAreCoercionErrors(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type _Decoded struct {
			Port int `json:"port"`
		}
		var out _Decoded
		expectError(t, resolveInto(t.Context(), &out, mustJaml(t, sb, "port: ${'eighty'}\n")), CoercionFailed, "$.port: json: cannot unmarshal string into Go struct field _Decoded.port")
		var text string
		expectError(t, resolveInto(t.Context(), &text, mustJaml(t, sb, "v: ${1}\n").At("v")), CoercionFailed, "$.v")
	})
}

type _SelfEmbedding struct {
	*_SelfEmbedding
	V int `json:"v"`
}

type SelfEmbedding = _SelfEmbedding

func TestRecursiveEmbeddingTerminates(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var got = mustResolve[SelfEmbedding](t, t.Context(), mustJaml(t, sb, "v: 1\n"))
		expectEqual(t, got.V, 1)
	})
}

func TestATargetJSONCannotDecodeIntoIsRefused(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "a: 1\n")
		_, err := Resolve[func()](t.Context(), document)
		expectError(t, err, InvalidTarget, "func()")
		_, err = Resolve[chan int](t.Context(), document)
		expectError(t, err, InvalidTarget, "chan int")
		_, err = Resolve[error](t.Context(), document)
		expectError(t, err, InvalidTarget, "the interface error")
		_, err = Resolve[struct{ A Expression }](t.Context(), document)
		expectError(t, err, InvalidTarget, "never a jaml.Expression held by value")
		_, err = Resolve[map[string]**Expression](t.Context(), document)
		expectError(t, err, InvalidTarget, "never a jaml.Expression held by value")
	})
}

type _WrappingPort struct{ N int }

func (p *_WrappingPort) UnmarshalJSON(written []byte) error {
	var raw struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(written, &raw); err != nil {
		return fmt.Errorf("port: %w", err)
	}
	p.N = raw.Number
	return nil
}

type _DelegatingPort struct{ N int }

func (p *_DelegatingPort) UnmarshalJSON(written []byte) error {
	var raw struct {
		Number int `json:"number"`
	}
	return json.Unmarshal(written, &raw)
}

type _PortsConfig[P any] struct {
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
	var cases = map[string]func() any{
		"wrapping":   func() any { return new(_PortsConfig[_WrappingPort]) },
		"delegating": func() any { return new(_PortsConfig[_DelegatingPort]) },
	}
	for name, newOut := range cases {
		t.Run(name, func(t *testing.T) {
			var both = _Agreement{text: portsTemplate, path: []string{"config"}}.eval(t, newOut)
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

type _EmbeddingAndMap struct {
	EmbeddedPort
	Limits map[string]int `json:"limits"`
}

func TestBlameStillReachesEmbeddedFieldsAndMapValues(t *testing.T) {
	_Agreement{text: "port: x\nlimits: {cpu: 1}\n"}.eval(t, func() any { return new(_EmbeddingAndMap) }).expectRefused(t, CoercionFailed, "$.port: json: cannot unmarshal string")
	_Agreement{text: "port: 1\nlimits: {cpu: 1, mem: x}\n"}.eval(t, func() any { return new(_EmbeddingAndMap) }).expectRefused(t, CoercionFailed, "$.limits.mem: json: cannot unmarshal string")
}
