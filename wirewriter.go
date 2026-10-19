package jaml

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"
)

const closingSlack = 64

type _WireWriter struct {
	written []byte
	limit   int
}

type _PastLimit struct{}

func (_PastLimit) Error() string {
	return "the written JSON passed its limit"
}

var controlEscapes = func() [utf8.RuneSelf]string {
	var escapes [utf8.RuneSelf]string
	for code := range 0x20 {
		escapes[code] = fmt.Sprintf(`\u%04x`, code)
	}
	escapes['"'], escapes['\\'] = `\"`, `\\`
	escapes['\b'], escapes['\f'], escapes['\n'], escapes['\r'], escapes['\t'] = `\b`, `\f`, `\n`, `\r`, `\t`
	return escapes
}()

func unboundedWriter() *_WireWriter {
	return &_WireWriter{limit: math.MaxInt}
}

func (w *_WireWriter) raw(text string) error {
	if len(w.written)+len(text) > w.limit {
		return _PastLimit{}
	}
	w.reserve(len(text))
	w.written = append(w.written, text...)
	return nil
}

func (w *_WireWriter) mark(written byte) {
	w.reserve(1)
	w.written = append(w.written, written)
}

func (w *_WireWriter) reserve(more int) {
	ceiling := min(w.limit, math.MaxInt-1) + 1
	wanted := min(len(w.written)+more, ceiling)
	if cap(w.written) >= wanted {
		return
	}
	grown := make([]byte, len(w.written), min(max(wanted, 2*cap(w.written)), ceiling))
	copy(grown, w.written)
	w.written = grown
}

func (w *_WireWriter) data(value any) error {
	switch typed := value.(type) {
	case nil:
		return w.raw("null")
	case bool:
		return w.raw(strconv.FormatBool(typed))
	case int64:
		var digits [24]byte
		return w.raw(string(strconv.AppendInt(digits[:0], typed, 10)))
	case float64:
		return w.number(typed)
	case string:
		return w.quoted(typed)
	case []any:
		return w.list(typed)
	case map[string]any:
		return w.object(typed)
	case *_Mapping:
		return w.mapping(typed)
	}
	return fmt.Errorf("%T is not jaml data", value)
}

func (w *_WireWriter) number(number float64) error {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return fmt.Errorf("%s is not a JSON number", strconv.FormatFloat(number, 'g', -1, 64))
	}
	format := byte('f')
	if magnitude := math.Abs(number); magnitude != 0 && (magnitude < 1e-6 || magnitude >= 1e21) {
		format = 'e'
	}
	var digits [32]byte
	written := strconv.AppendFloat(digits[:0], number, format, -1, 64)
	if end := len(written); format == 'e' && end >= 4 && written[end-4] == 'e' && written[end-3] == '-' && written[end-2] == '0' {
		written[end-2] = written[end-1]
		written = written[:end-1]
	}
	return w.raw(string(written))
}

func (w *_WireWriter) quoted(text string) error {
	length := quotedLength(text)
	if len(w.written)+length > w.limit {
		return _PastLimit{}
	}
	w.reserve(length + closingSlack)
	w.written = append(w.written, '"')
	safe := 0
	for index := 0; index < len(text); {
		escape, width := escapeAt(text, index)
		if escape != "" {
			w.written = append(append(w.written, text[safe:index]...), escape...)
			safe = index + width
		}
		index += width
	}
	w.written = append(append(w.written, text[safe:]...), '"')
	return nil
}

func quotedLength(text string) int {
	length := 2
	for index := 0; index < len(text); {
		escape, width := escapeAt(text, index)
		length += max(len(escape), width)
		index += width
	}
	return length
}

func escapeAt(text string, index int) (string, int) {
	if code := text[index]; code < utf8.RuneSelf {
		return controlEscapes[code], 1
	}
	read, width := utf8.DecodeRuneInString(text[index:])
	switch {
	case read == utf8.RuneError && width == 1:
		return "\\ufffd", 1
	case read == 0x2028:
		return "\\u2028", width
	case read == 0x2029:
		return "\\u2029", width
	}
	return "", width
}

func (w *_WireWriter) list(list []any) error {
	if list == nil {
		return w.raw("null")
	}
	w.mark('[')
	for index, item := range list {
		if index > 0 {
			w.mark(',')
		}
		if err := w.data(item); err != nil {
			return err
		}
	}
	return w.raw("]")
}

func (w *_WireWriter) object(object map[string]any) error {
	if object == nil {
		return w.raw("null")
	}
	w.mark('{')
	for index, key := range slices.Sorted(maps.Keys(object)) {
		if err := w.field(index, key, object[key]); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (w *_WireWriter) mapping(mapping *_Mapping) error {
	w.mark('{')
	for index, key := range mapping.keys {
		if err := w.field(index, key, mapping.values[key]); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (w *_WireWriter) field(index int, key string, value any) error {
	if index > 0 {
		w.mark(',')
	}
	if err := w.quoted(key); err != nil {
		return err
	}
	w.mark(':')
	return w.data(value)
}
