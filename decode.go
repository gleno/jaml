package jaml

import (
	"bytes"
	"encoding/json"
	"errors"
)

func (p *dataPath) blame(encoded []byte, err error) *dataPath {
	var mismatch *json.UnmarshalTypeError
	if !errors.As(err, &mismatch) {
		return p
	}
	return p.within(encoded, mismatch.Offset)
}

type jsonFrame struct {
	at        *dataPath
	object    bool
	key       string
	expectKey bool
	index     int
}

func (p *dataPath) within(encoded []byte, offset int64) *dataPath {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var open []*jsonFrame
	for {
		token, err := decoder.Token()
		if err != nil {
			return p
		}
		delim, isDelim := token.(json.Delim)
		if isDelim && (delim == '}' || delim == ']') {
			open = open[:len(open)-1]
			continue
		}
		valueAt := p
		if len(open) > 0 {
			top := open[len(open)-1]
			switch {
			case top.object && top.expectKey:
				top.key, top.expectKey = token.(string), false
				continue
			case top.object:
				valueAt, top.expectKey = top.at.atKey(top.key), true
			default:
				valueAt = top.at.atIndex(top.index)
				top.index++
			}
		}
		if decoder.InputOffset() >= offset {
			return valueAt
		}
		if isDelim {
			open = append(open, &jsonFrame{at: valueAt, object: delim == '{', expectKey: true})
		}
	}
}
