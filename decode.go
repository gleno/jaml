package jaml

import (
	"bytes"
	"encoding/json"
	"strings"
)

func (p *dataPath) blame(encoded []byte, err error) *dataPath {
	mismatch, isOwn := err.(*json.UnmarshalTypeError)
	if !isOwn {
		return p
	}
	found, token := p.within(encoded, mismatch.Offset)
	if found == nil || !holdsKind(token, mismatch.Value) || !namesField(found, mismatch.Field) {
		return p
	}
	return found
}

func holdsKind(token json.Token, kind string) bool {
	switch typed := token.(type) {
	case string:
		return kind == "string"
	case float64:
		return kind == "number" || strings.HasPrefix(kind, "number ")
	case bool:
		return kind == "bool"
	case json.Delim:
		return typed == '{' && kind == "object" || typed == '[' && kind == "array"
	}
	return false
}

func namesField(found *dataPath, field string) bool {
	if field == "" {
		return true
	}
	last := field[strings.LastIndexByte(field, '.')+1:]
	for step := found; step != nil; step = step.parent {
		if step.keyed && step.key == last {
			return true
		}
	}
	return false
}

type jsonFrame struct {
	at        *dataPath
	object    bool
	key       string
	expectKey bool
	index     int
}

func (p *dataPath) within(encoded []byte, offset int64) (*dataPath, json.Token) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var open []*jsonFrame
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil
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
			return valueAt, token
		}
		if isDelim {
			open = append(open, &jsonFrame{at: valueAt, object: delim == '{', expectKey: true})
		}
	}
}
