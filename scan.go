package jaml

import (
	"errors"
	"strings"
)

var (
	errUnclosed            = errors.New("unclosed ${")
	errUnclosedDouble      = errors.New("unclosed ${{: interpolations close with }}, and an object literal is written ${ {a: 1} }")
	errEmptyExpression     = errors.New("empty expression")
	errNotAWholeExpression = errors.New("a value that starts with ${ must be one whole expression; interpolate into text with ${{ }}")
)

type scanned struct {
	whole    string
	segments []scannedSegment
}

type scannedSegment struct {
	text string
	code bool
}

func scan(text string) (scanned, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "${") && !strings.HasPrefix(trimmed, "${{") {
		return scanWhole(trimmed)
	}
	segments, err := scanInterpolations(text)
	return scanned{segments: segments}, err
}

func scanWhole(trimmed string) (scanned, error) {
	end, err := closingBrace(trimmed, 2)
	if err != nil {
		return scanned{}, err
	}
	if end != len(trimmed)-1 {
		return scanned{}, errNotAWholeExpression
	}
	code := trimmed[2:end]
	if strings.TrimSpace(code) == "" {
		return scanned{}, errEmptyExpression
	}
	return scanned{whole: code}, nil
}

func scanInterpolations(text string) ([]scannedSegment, error) {
	var segments []scannedSegment
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			if text != "" {
				segments = append(segments, scannedSegment{text: text})
			}
			return segments, nil
		}
		if start > 0 {
			segments = append(segments, scannedSegment{text: text[:start]})
		}
		end, err := closingBrace(text, start+3)
		if err != nil || end+1 >= len(text) || text[end+1] != '}' {
			return nil, errUnclosedDouble
		}
		code := text[start+3 : end]
		if strings.TrimSpace(code) == "" {
			return nil, errEmptyExpression
		}
		segments = append(segments, scannedSegment{text: code, code: true})
		text = text[end+2:]
	}
}

func closingBrace(text string, from int) (int, error) {
	depth := 0
	for i := from; i < len(text); i++ {
		var err error
		switch {
		case text[i] == '\'' || text[i] == '"':
			i, err = skipQuoted(text, i)
		case text[i] == '`':
			i, err = skipTemplateLiteral(text, i)
		case strings.HasPrefix(text[i:], "//"):
			i = skipLineComment(text, i)
		case strings.HasPrefix(text[i:], "/*"):
			i, err = skipBlockComment(text, i)
		case text[i] == '{':
			depth++
		case text[i] == '}':
			if depth == 0 {
				return i, nil
			}
			depth--
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, errUnclosed
}

func skipQuoted(text string, open int) (int, error) {
	for i := open + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case text[open]:
			return i, nil
		}
	}
	return 0, errUnclosed
}

func skipTemplateLiteral(text string, open int) (int, error) {
	for i := open + 1; i < len(text); i++ {
		switch {
		case text[i] == '\\':
			i++
		case text[i] == '`':
			return i, nil
		case strings.HasPrefix(text[i:], "${"):
			end, err := closingBrace(text, i+2)
			if err != nil {
				return 0, err
			}
			i = end
		}
	}
	return 0, errUnclosed
}

func skipLineComment(text string, open int) int {
	end := strings.IndexByte(text[open:], '\n')
	if end < 0 {
		return len(text) - 1
	}
	return open + end
}

func skipBlockComment(text string, open int) (int, error) {
	end := strings.Index(text[open+2:], "*/")
	if end < 0 {
		return 0, errUnclosed
	}
	return open + 2 + end + 1, nil
}
