package jaml

import (
	"fmt"

	"github.com/gleno/fault"
)

type text struct {
	whole    *expression
	segments []segment
}

type segment struct {
	literal string
	code    *expression
}

func compileTexts(root any) (map[string]text, error) {
	texts := map[string]text{}
	var visit func(node any, at string) error
	visit = func(node any, at string) error {
		source, isString := node.(string)
		if !isString {
			return eachChild(node, at, visit)
		}
		if _, seen := texts[source]; seen {
			return nil
		}
		compiled, hasCode, err := compileText(source)
		if err != nil {
			return fault.From(InvalidTemplate, fmt.Sprintf("%s: %s", at, err))
		}
		if hasCode {
			texts[source] = compiled
		}
		return nil
	}
	return texts, visit(root, "$")
}

func compileText(source string) (text, bool, error) {
	found, err := scan(source)
	if err != nil {
		return text{}, false, err
	}
	if found.whole != "" {
		compiled, err := compileExpression(found.whole, false)
		if err != nil {
			return text{}, false, fmt.Errorf("${%s}: %w", preview(found.whole), err)
		}
		return text{whole: &compiled}, true, nil
	}
	return compileSegments(found.segments)
}

func compileSegments(scanned []scannedSegment) (text, bool, error) {
	compiled := text{segments: make([]segment, 0, len(scanned))}
	hasCode := false
	for _, piece := range scanned {
		if !piece.code {
			compiled.segments = append(compiled.segments, segment{literal: piece.text})
			continue
		}
		code, err := compileExpression(piece.text, true)
		if err != nil {
			return text{}, false, fmt.Errorf("${{%s}}: %w", preview(piece.text), err)
		}
		hasCode = true
		compiled.segments = append(compiled.segments, segment{code: &code})
	}
	return compiled, hasCode, nil
}
