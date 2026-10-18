package jaml

import "encoding/json"

const (
	evaluatorMarker      = "JAML_EVALUATOR"
	evaluatorMemoryLimit = "JAML_MEMORY_LIMIT"
	exitMemoryLimit      = 86
	requestsFD           = 3
	responsesFD          = 4
)

type operation string

const (
	opCompile operation = "compile"
	opOpen    operation = "open"
	opEval    operation = "eval"
	opClose   operation = "close"
)

type request struct {
	Op      operation   `json:"op"`
	Sources []source    `json:"sources,omitempty"`
	Vars    encodedVars `json:"vars,omitempty"`
	ID      int         `json:"id,omitempty"`
}

type response struct {
	Value     json.RawMessage `json:"value,omitempty"`
	Undefined bool            `json:"undefined,omitempty"`
	Error     string          `json:"error,omitempty"`
}
