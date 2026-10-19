package jaml

import (
	"bytes"
	"encoding/json"
	"strings"
)

func blame(path string, encoded []byte, err error) string {
	mismatch, isOwn := err.(*json.UnmarshalTypeError)
	if !isOwn {
		return path
	}
	found, token, reached := mismatchedValue(encoded, mismatch.Offset)
	if !reached || !holdsKind(token, mismatch.Value) || !namesField(found, mismatch.Field) {
		return path
	}
	return found.spelledFrom(path)
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
	var last = field[strings.LastIndexByte(field, '.')+1:]
	for step := found; step != nil; step = step.parent {
		if step.keyed && step.key == last {
			return true
		}
	}
	return false
}

type _JsonFrame struct {
	path      *_DataPath
	object    bool
	key       string
	expectKey bool
	index     int
}

func mismatchedValue(encoded []byte, offset int64) (*_DataPath, json.Token, bool) {
	var root *_DataPath
	var decoder = json.NewDecoder(bytes.NewReader(encoded))
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
		var valuePath = root
		if len(open) > 0 {
			var top = open[len(open)-1]
			switch {
			case top.object && top.expectKey:
				top.key, top.expectKey = token.(string), false
				continue
			case top.object:
				valuePath, top.expectKey = top.path.atKey(top.key), true
			default:
				valuePath = top.path.atIndex(top.index)
				top.index++
			}
		}
		if decoder.InputOffset() >= offset {
			return valuePath, token, true
		}
		if isDelim {
			open = append(open, &_JsonFrame{path: valuePath, object: delim == '{', expectKey: true})
		}
	}
}
