package jaml

import (
	"bytes"
	"encoding/json"
	"strings"
)

func blame(at string, encoded []byte, err error) string {
	mismatch, isOwn := err.(*json.UnmarshalTypeError)
	if !isOwn {
		return at
	}
	found, token, reached := mismatchedValue(encoded, mismatch.Offset)
	if !reached || !holdsKind(token, mismatch.Value) || !namesField(found, mismatch.Field) {
		return at
	}
	return found.spelledFrom(at)
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

func namesField(found *_DataPath, field string) bool {
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

type _JsonFrame struct {
	at        *_DataPath
	object    bool
	key       string
	expectKey bool
	index     int
}

func mismatchedValue(encoded []byte, offset int64) (*_DataPath, json.Token, bool) {
	var root *_DataPath
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var open []*_JsonFrame
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, false
		}
		delim, isDelim := token.(json.Delim)
		if isDelim && (delim == '}' || delim == ']') {
			open = open[:len(open)-1]
			continue
		}
		valueAt := root
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
			return valueAt, token, true
		}
		if isDelim {
			open = append(open, &_JsonFrame{at: valueAt, object: delim == '{', expectKey: true})
		}
	}
}
