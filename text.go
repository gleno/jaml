package jaml

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type text struct {
	whole    *expression
	segments []segment
}

type segment struct {
	literal string
	code    *expression
}

type compiler struct {
	texts map[string]text
}

func compileTexts(root any) (map[string]text, error) {
	c := &compiler{texts: map[string]text{}}
	if err := c.visit(root, nil); err != nil {
		return nil, err
	}
	return c.texts, nil
}

var errNotAWholeExpression = errors.New("a value that starts with ${ must be one whole expression; interpolate into text with ${{ }}")

type delimiter struct {
	opens  string
	closes string
}

var (
	wholeValue    = delimiter{opens: "${", closes: "}"}
	interpolation = delimiter{opens: "${{", closes: "}}"}
)

func (d delimiter) around(code string) string {
	return d.opens + preview(code) + d.closes
}

func (c *compiler) visit(node any, at *dataPath) error {
	written, isString := node.(string)
	if !isString {
		return eachChild(node, at, c.visit)
	}
	if _, seen := c.texts[written]; seen {
		return nil
	}
	compiled, err := c.compileText(written)
	if err != nil {
		return fail(InvalidTemplate, "%s: %w", at, err)
	}
	if compiled.holdsCode() {
		c.texts[written] = compiled
	}
	return nil
}

func (t text) holdsCode() bool {
	return t.whole != nil || slices.ContainsFunc(t.segments, func(piece segment) bool { return piece.code != nil })
}

func (c *compiler) compileText(written string) (text, error) {
	trimmed := strings.TrimSpace(written)
	whole := strings.HasPrefix(trimmed, wholeValue.opens) && !strings.HasPrefix(trimmed, interpolation.opens)
	if !whole && !strings.Contains(written, interpolation.opens) {
		return text{}, nil
	}
	if err := refuseLongCode(written); err != nil {
		return text{}, err
	}
	if whole {
		return c.compileWhole(trimmed)
	}
	return c.compileInterpolations(written)
}

func (c *compiler) compileWhole(trimmed string) (text, error) {
	after := trimmed[len(wholeValue.opens):]
	end, err := searchingEndsIn(trimmed).expressionEnd(after, wholeValue)
	if err != nil {
		return text{}, fmt.Errorf("%s%s: %w", wholeValue.opens, preview(after), err)
	}
	if end != len(after)-len(wholeValue.closes) {
		return text{}, errNotAWholeExpression
	}
	code := after[:end]
	compiled, err := compileExpression(code, false)
	if err != nil {
		return text{}, fmt.Errorf("%s: %w", wholeValue.around(code), err)
	}
	return text{whole: &compiled}, nil
}

func (c *compiler) compileInterpolations(written string) (text, error) {
	var compiled text
	search := searchingEndsIn(written)
	rest := written
	for {
		start := strings.Index(rest, interpolation.opens)
		if start < 0 {
			return compiled.withLiteral(rest), nil
		}
		compiled = compiled.withLiteral(rest[:start])
		after := rest[start+len(interpolation.opens):]
		end, err := search.expressionEnd(after, interpolation)
		if err != nil {
			return text{}, fmt.Errorf("%s%s: %w", interpolation.opens, preview(after), err)
		}
		code, err := compileExpression(after[:end], true)
		if err != nil {
			return text{}, fmt.Errorf("%s: %w", interpolation.around(after[:end]), err)
		}
		compiled.segments = append(compiled.segments, segment{code: &code})
		rest = after[end+len(interpolation.closes):]
	}
}

func (t text) withLiteral(literal string) text {
	if literal == "" {
		return t
	}
	t.segments = append(t.segments, segment{literal: literal})
	return t
}
