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

type source struct {
	Text         string `json:"text"`
	Interpolated bool   `json:"interpolated,omitempty"`
}

type compiler struct {
	texts   map[string]text
	sources []source
}

func compileTexts(root any) (map[string]text, []source, error) {
	c := &compiler{texts: map[string]text{}}
	if err := c.visit(root, nil); err != nil {
		return nil, nil, err
	}
	return c.texts, c.sources, nil
}

func (c *compiler) visit(node any, at *dataPath) error {
	written, isString := node.(string)
	if !isString {
		return eachChild(node, at, c.visit)
	}
	if _, seen := c.texts[written]; seen {
		return nil
	}
	compiled, hasCode, err := c.compileText(written)
	if err != nil {
		return fault.From(InvalidTemplate, fmt.Sprintf("%s: %s", at, err))
	}
	if hasCode {
		c.texts[written] = compiled
	}
	return nil
}

func (c *compiler) compileText(written string) (text, bool, error) {
	found, err := scan(written)
	if err != nil {
		return text{}, false, err
	}
	if found.whole != "" {
		compiled, err := c.compile(source{Text: found.whole})
		if err != nil {
			return text{}, false, fmt.Errorf("${%s}: %w", preview(found.whole), err)
		}
		return text{whole: &compiled}, true, nil
	}
	return c.compileSegments(found.segments)
}

func (c *compiler) compileSegments(scanned []scannedSegment) (text, bool, error) {
	compiled := text{segments: make([]segment, 0, len(scanned))}
	hasCode := false
	for _, piece := range scanned {
		if !piece.code {
			compiled.segments = append(compiled.segments, segment{literal: piece.text})
			continue
		}
		code, err := c.compile(source{Text: piece.text, Interpolated: true})
		if err != nil {
			return text{}, false, fmt.Errorf("${{%s}}: %w", preview(piece.text), err)
		}
		hasCode = true
		compiled.segments = append(compiled.segments, segment{code: &code})
	}
	return compiled, hasCode, nil
}

func (c *compiler) compile(written source) (expression, error) {
	compiled, err := compileExpression(written.Text, written.Interpolated)
	if err != nil {
		return expression{}, err
	}
	compiled.id = len(c.sources)
	c.sources = append(c.sources, written)
	return compiled, nil
}
