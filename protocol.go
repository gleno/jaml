package jaml

import "encoding/json"

const (
	evaluatorMarker      = "JAML_EVALUATOR"
	evaluatorMemoryLimit = "JAML_MEMORY_LIMIT"
	evaluatorNonce       = "JAML_NONCE"
	evaluatorParent      = "JAML_PARENT"
	evaluatorMagic       = "jaml-evaluator"
	protocolVersion      = 1
	exitMemoryLimit      = 86
	requestsFD           = 3
	responsesFD          = 4
)

type hello struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Nonce   string `json:"nonce"`
}

type operation string

const (
	opCompile operation = "compile"
	opOpen    operation = "open"
	opEval    operation = "eval"
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
