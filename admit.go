package jaml

import (
	"fmt"
	"unicode/utf8"
)

func admitJamlText(text string) error {
	if utf8.ValidString(text) {
		return nil
	}
	return fail(InvalidExpression, "yaml: %s", encodingProblem(text))
}

func admitSource(source string) error {
	if utf8.ValidString(source) {
		return nil
	}
	return failWith(InvalidExpression, _Quoted{code: bare.around(source), reason: syntaxErrorAt(source, firstInvalidByte(source), "Invalid UTF-8 character")})
}

func admitPath(path []string) error {
	for index, step := range path {
		if !utf8.ValidString(step) {
			return missingPath(selectedPath(path[:index]), fmt.Sprintf("has no key %q", step))
		}
	}
	return nil
}

func firstInvalidByte(text string) int {
	for offset := 0; offset < len(text); {
		decoded, width := utf8.DecodeRuneInString(text[offset:])
		if decoded == utf8.RuneError && width <= 1 {
			return offset
		}
		offset += width
	}
	return len(text)
}

func encodingProblem(text string) string {
	var offset = firstInvalidByte(text)
	var leading = text[offset]
	width, value := sequenceShape(leading)
	switch {
	case width == 0:
		return "invalid leading UTF-8 octet"
	case offset+width > len(text):
		return "incomplete UTF-8 octet sequence"
	}
	for _, trailing := range []byte(text[offset+1 : offset+width]) {
		if trailing&0xC0 != 0x80 {
			return "invalid trailing UTF-8 octet"
		}
		value = value<<6 + rune(trailing&0x3F)
	}
	if value < [...]rune{0, 0, 0x80, 0x800, 0x10000}[width] {
		return "invalid length of a UTF-8 sequence"
	}
	return "invalid Unicode character"
}

func sequenceShape(leading byte) (int, rune) {
	switch {
	case leading&0xE0 == 0xC0:
		return 2, rune(leading & 0x1F)
	case leading&0xF0 == 0xE0:
		return 3, rune(leading & 0x0F)
	case leading&0xF8 == 0xF0:
		return 4, rune(leading & 0x07)
	}
	return 0, 0
}
