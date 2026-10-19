package jaml

import (
	"encoding/json"

	"github.com/dop251/goja"
)

type Thrown struct {
	Message string
	Value   any
}

func (t *Thrown) Error() string {
	return t.Message
}

func newThrown(message string, encoded json.RawMessage) *Thrown {
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		value = nil
	}
	return &Thrown{Message: message, Value: value}
}

type thrownPolicy struct{}

func (thrownPolicy) stranger(part notData) (any, error) {
	if part.leftOut {
		return undefined{}, nil
	}
	return nil, nil
}

func (thrownPolicy) instance(func() error) (bool, error) {
	return true, nil
}

func (r realm) snapshot(thrown goja.Value) json.RawMessage {
	var taken any
	var err error
	if contained := r.contain(func() { taken, err = readData(r, thrown, thrownPolicy{}) }); contained != nil || err != nil {
		return json.RawMessage("null")
	}
	if _, isUndefined := taken.(undefined); isUndefined {
		return json.RawMessage("null")
	}
	encoded, err := wireJSON(taken)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}
