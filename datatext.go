package jaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

func dataText(value any) (string, error) {
	var text strings.Builder
	if err := writeData(&text, value); err != nil {
		return "", err
	}
	return text.String(), nil
}

func writeData(text *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		text.WriteString("null")
	case bool:
		text.WriteString(strconv.FormatBool(typed))
	case string:
		writeQuoted(text, typed)
	case int64:
		text.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		writeNumber(text, typed)
	case json.Number:
		text.WriteString(typed.String())
	case []any:
		return writeList(text, typed)
	case map[string]any:
		return writeObject(text, typed)
	default:
		return fmt.Errorf("%T is not jaml data", value)
	}
	return nil
}

func writeList(text *strings.Builder, list []any) error {
	text.WriteByte('[')
	for i, item := range list {
		if i > 0 {
			text.WriteByte(',')
		}
		if err := writeData(text, item); err != nil {
			return err
		}
	}
	text.WriteByte(']')
	return nil
}

func writeObject(text *strings.Builder, object map[string]any) error {
	text.WriteByte('{')
	for i, key := range slices.Sorted(maps.Keys(object)) {
		if i > 0 {
			text.WriteByte(',')
		}
		writeQuoted(text, key)
		text.WriteByte(':')
		if err := writeData(text, object[key]); err != nil {
			return err
		}
	}
	text.WriteByte('}')
	return nil
}

func writeNumber(text *strings.Builder, number float64) {
	format := byte('f')
	if magnitude := math.Abs(number); magnitude != 0 && (magnitude < 1e-6 || magnitude >= 1e21) {
		format = 'e'
	}
	written := strconv.AppendFloat(nil, number, format, -1, 64)
	if format == 'e' {
		written = bytes.Replace(written, []byte("e-0"), []byte("e-"), 1)
	}
	text.Write(written)
}

func writeQuoted(text *strings.Builder, written string) {
	text.WriteByte('"')
	for _, r := range written {
		switch {
		case r == '"' || r == '\\':
			text.WriteByte('\\')
			text.WriteRune(r)
		case r == '\b':
			text.WriteString(`\b`)
		case r == '\f':
			text.WriteString(`\f`)
		case r == '\n':
			text.WriteString(`\n`)
		case r == '\r':
			text.WriteString(`\r`)
		case r == '\t':
			text.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(text, `\u%04x`, r)
		default:
			text.WriteRune(r)
		}
	}
	text.WriteByte('"')
}
