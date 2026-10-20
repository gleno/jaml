package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strconv"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	loneLow        = 0xDC00
	maxPlainDigits = 308
)

type _Datum struct {
	encoded json.RawMessage
}

type _BoundValues map[string]_Datum

var refusedNames = sync.OnceValue(func() map[string]bool {
	var names = map[string]bool{}
	opened, err := openSession(context.Background(), _SessionPlan{runtime: _Runtime{builtins: mutableBuiltins}})
	if err != nil {
		panic(err)
	}
	defer opened.close()
	err = opened.enter(func(r _Realm) error {
		for object := r.vm.GlobalObject(); object != nil; object = object.Prototype() {
			for _, name := range object.GetOwnPropertyNames() {
				names[name] = true
			}
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return names
})

func admitName(name string) error {
	var shown = clip(name, keyExcerptLength)
	if !utf8.ValidString(name) {
		return fmt.Errorf("value %q is not named in valid UTF-8", shown)
	}
	if refusedNames()[name] {
		return fmt.Errorf("value %q collides with a JavaScript global", shown)
	}
	return nil
}

func checkDatum(encoded []byte) error {
	var wellFormed = true
	var deepest = walkJSON(encoded, func(text []byte) { wellFormed = wellFormed && admissibleText(text) })
	switch {
	case deepest > maxDataDepth:
		return fmt.Errorf("%s", nestsTooDeep)
	case !wellFormed:
		return fmt.Errorf("holds text that is not valid UTF-8")
	}
	if number := numberOutOfRange(encoded); number != nil {
		return fmt.Errorf("holds the number %s, which is out of range", clip(string(number), keyExcerptLength))
	}
	return nil
}

func numberOutOfRange(encoded []byte) []byte {
	for i := 0; i < len(encoded); i++ {
		switch written := encoded[i]; {
		case written == '"':
			i = textEnd(encoded, i+1)
		case written == '-' || written >= '0' && written <= '9':
			var start = i
			for i < len(encoded) && isNumberByte(encoded[i]) {
				i++
			}
			var number = encoded[start:i]
			if len(number) > maxPlainDigits || bytes.ContainsAny(number, "eE") {
				if _, err := strconv.ParseFloat(string(number), 64); err != nil {
					return number
				}
			}
		}
	}
	return nil
}

func (d _Datum) MarshalJSON() ([]byte, error) {
	return d.encoded, nil
}

func (d *_Datum) UnmarshalJSON(written []byte) error {
	if err := checkDatum(written); err != nil {
		return fmt.Errorf("a datum %w", err)
	}
	d.encoded = written[:len(written):len(written)]
	return nil
}

func (v *_BoundValues) UnmarshalJSON(written []byte) error {
	var decoded map[string]_Datum
	if err := json.Unmarshal(written, &decoded); err != nil {
		return err
	}
	for name := range decoded {
		if err := admitName(name); err != nil {
			return err
		}
	}
	*v = decoded
	return nil
}

type _JSONText interface{ ~string | ~[]byte }

func jsonDepth[T _JSONText](encoded T) int {
	return walkJSON(encoded, nil)
}

func walkJSON[T _JSONText](encoded T, onText func(text T)) int {
	depth, deepest := 0, 0
	for i := 0; i < len(encoded); i++ {
		switch encoded[i] {
		case '"':
			var end = textEnd(encoded, i+1)
			if onText != nil {
				onText(encoded[i+1 : end])
			}
			i = end
		case '[', '{':
			depth++
			deepest = max(deepest, depth)
		case ']', '}':
			depth--
		}
	}
	return deepest
}

func textEnd[T _JSONText](encoded T, start int) int {
	for i := start; i < len(encoded); i++ {
		switch encoded[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(encoded)
}

func admissibleText(text []byte) bool {
	if !utf8.Valid(text) {
		return false
	}
	if bytes.IndexByte(text, '\\') < 0 {
		return true
	}
	var replaced = false
	var units = func(yield func(uint16) bool) {
		for i := 0; i < len(text); {
			unit, size := nextUnit(text[i:])
			replaced = replaced || size == len(`\ufffd`) && unit == utf8.RuneError
			if !yield(unit) {
				return
			}
			if written, _ := utf8.DecodeRune(text[i:]); written > 0xFFFF {
				_, low := utf16.EncodeRune(written)
				if !yield(uint16(low)) {
					return
				}
			}
			i += size
		}
	}
	return wellFormedUnits(units) && !replaced
}

func nextUnit(text []byte) (uint16, int) {
	switch {
	case text[0] != '\\':
		written, size := utf8.DecodeRune(text)
		if written > 0xFFFF {
			high, _ := utf16.EncodeRune(written)
			return uint16(high), size
		}
		return uint16(written), size
	case len(text) >= len(`\u0000`) && text[1] == 'u':
		unit, err := strconv.ParseUint(string(text[2:6]), 16, 16)
		if err != nil {
			return loneLow, len(text)
		}
		return uint16(unit), len(`\u0000`)
	case len(text) >= 2:
		return uint16(text[1]), 2
	}
	return loneLow, len(text)
}

func wellFormedUnits(units iter.Seq[uint16]) bool {
	var awaitingLow = false
	for unit := range units {
		var low = unit >= loneLow && unit <= 0xDFFF
		switch {
		case awaitingLow && !low:
			return false
		case awaitingLow:
			awaitingLow = false
		case low:
			return false
		default:
			awaitingLow = unit >= 0xD800 && unit < loneLow
		}
	}
	return !awaitingLow
}
