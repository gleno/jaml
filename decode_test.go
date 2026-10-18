package jaml

import (
	"encoding/json"
	"testing"
)

func TestTargetsDecodeThroughEncodingJSON(t *testing.T) {
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
	template := mustTemplate(t, `
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
}

func TestMismatchedTypesAreCoercionErrors(t *testing.T) {
	type shape struct {
		Port int `json:"port"`
	}
	var out shape
	expectError(t, mustTemplate(t, "port: ${'eighty'}\n").Eval(&out, nil), CoercionFailed, "$", "shape.port")
	var text string
	expectError(t, mustTemplate(t, "v: ${1}\n").Eval(&text, nil, "v"), CoercionFailed, "$.v")
}

type selfEmbedding struct {
	*SelfEmbedding
	V int `json:"v"`
}

type SelfEmbedding = selfEmbedding

func TestRecursiveEmbeddingTerminates(t *testing.T) {
	got := evalAs[SelfEmbedding](t, mustTemplate(t, "v: 1\n"), nil)
	expectEqual(t, got.V, 1)
}

func TestTargetMustBeANonNilPointer(t *testing.T) {
	template := mustTemplate(t, "a: 1\n")
	var out map[string]any
	expectError(t, template.Eval(out, nil), InvalidTarget, "map[string]interface {}")
	expectError(t, template.Eval(nil, nil), InvalidTarget, "nil")
	expectError(t, template.Eval((*int)(nil), nil), InvalidTarget, "nil *int")
}
