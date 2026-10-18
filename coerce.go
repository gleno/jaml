package jaml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"

	"github.com/gleno/fault"
)

const describedTextLength = 40

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

func (e *evaluation) stringify(resolved value, at location) (string, error) {
	switch typed := resolved.(type) {
	case stringValue:
		return string(typed), nil
	case boolValue:
		return typed.text(), nil
	case numberValue:
		return typed.asWritten(), nil
	case unrepresentable:
		return typed.raw, nil
	default:
		var encoded bytes.Buffer
		if err := e.writeJSON(&encoded, resolved, at); err != nil {
			return "", err
		}
		return encoded.String(), nil
	}
}

func (e *evaluation) writeJSON(out *bytes.Buffer, v value, at location) error {
	resolved, err := e.resolve(v, at)
	if err != nil {
		return err
	}
	switch typed := resolved.(type) {
	case nullValue:
		out.WriteString("null")
	case undefinedValue:
		if e.settings.undefined == Strict {
			return fault.From(CoercionFailed, fmt.Sprintf("%s: strict undefined coercion refuses undefined inside text", at))
		}
		out.WriteString("null")
	case boolValue:
		out.WriteString(strconv.FormatBool(typed.truth))
	case numberValue:
		out.WriteString(typed.text)
	case unrepresentable:
		return fault.From(CoercionFailed, fmt.Sprintf("%s: %s", at, typed.reason))
	case stringValue:
		writeJSONString(out, string(typed))
	case listValue:
		return e.writeJSONList(out, typed, at)
	case objectValue:
		return e.writeJSONObject(out, typed, at)
	}
	return nil
}

func (e *evaluation) writeJSONList(out *bytes.Buffer, list listValue, at location) error {
	out.WriteByte('[')
	for i, item := range list {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := e.writeJSON(out, item.value, at.index(i, item.at)); err != nil {
			return err
		}
	}
	out.WriteByte(']')
	return nil
}

func (e *evaluation) writeJSONObject(out *bytes.Buffer, object objectValue, at location) error {
	out.WriteByte('{')
	for i, entry := range object {
		if i > 0 {
			out.WriteByte(',')
		}
		writeJSONString(out, entry.key)
		out.WriteByte(':')
		if err := e.writeJSON(out, entry.value, at.key(entry.key, entry.at)); err != nil {
			return err
		}
	}
	out.WriteByte('}')
	return nil
}

func writeJSONString(out *bytes.Buffer, text string) {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	out.Truncate(out.Len() - 1)
}

func decodeBool(resolved value, into destination) error {
	switch typed := resolved.(type) {
	case boolValue:
		into.target.SetBool(typed.truth)
		return nil
	case stringValue:
		switch typed {
		case "true":
			into.target.SetBool(true)
			return nil
		case "false":
			into.target.SetBool(false)
			return nil
		}
		return mismatch(resolved, into, "only \"true\" and \"false\" read as bools")
	default:
		return mismatch(resolved, into, "")
	}
}

func numberText(resolved value, into destination) (string, error) {
	switch typed := resolved.(type) {
	case numberValue:
		return typed.text, nil
	case unrepresentable:
		return "", fault.From(CoercionFailed, fmt.Sprintf("%s: %s", into.at, typed.reason))
	case stringValue:
		if jsonNumber.MatchString(string(typed)) {
			return string(typed), nil
		}
		return "", mismatch(resolved, into, "text reads as a number only when it is a JSON number")
	default:
		return "", mismatch(resolved, into, "")
	}
}

func decodeInt(resolved value, into destination) error {
	text, err := numberText(resolved, into)
	if err != nil {
		return err
	}
	whole, err := parseWhole(text)
	if err != nil {
		return mismatch(resolved, into, err.Error())
	}
	if !whole.IsInt64() || into.target.OverflowInt(whole.Int64()) {
		return mismatch(resolved, into, "out of range")
	}
	into.target.SetInt(whole.Int64())
	return nil
}

func decodeUint(resolved value, into destination) error {
	text, err := numberText(resolved, into)
	if err != nil {
		return err
	}
	whole, err := parseWhole(text)
	if err != nil {
		return mismatch(resolved, into, err.Error())
	}
	if whole.Sign() < 0 {
		return mismatch(resolved, into, "negative")
	}
	if !whole.IsUint64() || into.target.OverflowUint(whole.Uint64()) {
		return mismatch(resolved, into, "out of range")
	}
	into.target.SetUint(whole.Uint64())
	return nil
}

func decodeFloat(resolved value, into destination) error {
	text, err := numberText(resolved, into)
	if err != nil {
		return err
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || into.target.OverflowFloat(number) {
		return mismatch(resolved, into, "out of range")
	}
	into.target.SetFloat(number)
	return nil
}

func naturalNumber(text string) any {
	if whole, err := strconv.ParseInt(text, 10, 64); err == nil {
		return whole
	}
	number, _ := strconv.ParseFloat(text, 64)
	if number == math.Trunc(number) && math.Abs(number) < 1<<63 {
		return int64(number)
	}
	return number
}

func describeValue(resolved value) string {
	switch typed := resolved.(type) {
	case stringValue:
		text := string(typed)
		if len(text) > describedTextLength {
			text = text[:describedTextLength] + "…"
		}
		return "string " + strconv.Quote(text)
	case numberValue:
		return "number " + typed.asWritten()
	case boolValue:
		return "bool " + typed.text()
	default:
		return resolved.kind()
	}
}

func parseWhole(text string) (*big.Int, error) {
	if whole, ok := new(big.Int).SetString(text, 10); ok {
		return whole, nil
	}
	number, _, err := big.ParseFloat(text, 10, 1024, big.ToNearestEven)
	if err != nil {
		return nil, err
	}
	if !number.IsInt() {
		return nil, errors.New("it has a fractional part")
	}
	whole, _ := number.Int(nil)
	return whole, nil
}
