package jaml

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type _Text struct {
	whole    *_Expression
	segments []_Segment
}

type _Segment struct {
	literal string
	code    *_Expression
}

type _Compiler struct {
	ctx        context.Context
	texts      map[string]_Text
	visited    map[any]bool
	delimiters _Delimiters
}

func compileTexts(ctx context.Context, root any, delimiters _Delimiters) (map[string]_Text, error) {
	var c = &_Compiler{ctx: ctx, texts: map[string]_Text{}, visited: map[any]bool{}, delimiters: delimiters}
	if err := c.visit(root, nil); err != nil {
		return nil, err
	}
	return c.texts, nil
}

type _Delimiter struct {
	opens  string
	closes string
}

type _Delimiters struct {
	value         _Delimiter
	interpolation _Delimiter
}

var bare = _Delimiter{}

func delimitersFor(sigil string) _Delimiters {
	return _Delimiters{value: _Delimiter{opens: sigil + "{", closes: "}"}, interpolation: _Delimiter{opens: sigil + "{{", closes: "}}"}}
}

func (d _Delimiters) notWhole() error {
	return fmt.Errorf("a value that starts with %s must be one whole expression; interpolate into text with %s }}", d.value.opens, d.interpolation.opens)
}

func (d _Delimiter) around(code string) string {
	return d.opens + excerpt(code, codeExcerptLength) + d.closes
}

func (c *_Compiler) visit(node any, path *_DataPath) error {
	written, isString := node.(string)
	if !isString {
		if c.revisits(node) {
			return nil
		}
		return eachChild(node, path, c.visit)
	}
	if _, seen := c.texts[written]; seen {
		return nil
	}
	if c.ctx.Err() != nil {
		return cancelled(c.ctx)
	}
	compiled, err := c.compileText(written)
	if err != nil {
		return fail(InvalidExpression, "%w", err).within(path.String())
	}
	if compiled.holdsCode() {
		c.texts[written] = compiled
	}
	return nil
}

func (c *_Compiler) revisits(node any) bool {
	var identity any
	switch typed := node.(type) {
	case *_Mapping:
		identity = typed
	case []any:
		if len(typed) == 0 {
			return false
		}
		identity = &typed[0]
	default:
		return false
	}
	if c.visited[identity] {
		return true
	}
	c.visited[identity] = true
	return false
}

func (t _Text) holdsCode() bool {
	return t.whole != nil || slices.ContainsFunc(t.segments, func(piece _Segment) bool { return piece.code != nil })
}

func (c *_Compiler) compileText(written string) (compiled _Text, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = failure
		}
	}()
	var trimmed = strings.TrimSpace(written)
	var whole = strings.HasPrefix(trimmed, c.delimiters.value.opens) && !strings.HasPrefix(trimmed, c.delimiters.interpolation.opens)
	if !whole && !strings.Contains(written, c.delimiters.interpolation.opens) {
		return _Text{}, nil
	}
	if err := refuseLongCode(written); err != nil {
		return _Text{}, err
	}
	if whole {
		return c.compileWhole(trimmed)
	}
	return c.compileInterpolations(written)
}

func (c *_Compiler) compileWhole(trimmed string) (_Text, error) {
	var wholeValue = c.delimiters.value
	var after = trimmed[len(wholeValue.opens):]
	end, err := searchingEndsIn(trimmed).expressionEnd(after, wholeValue)
	if err != nil {
		return _Text{}, fmt.Errorf("%s%s: %w", wholeValue.opens, excerpt(after, codeExcerptLength), err)
	}
	if end != len(after)-len(wholeValue.closes) {
		return _Text{}, c.delimiters.notWhole()
	}
	var code = after[:end]
	compiled, err := compileExpression(code, wholeValue)
	if err != nil {
		return _Text{}, fmt.Errorf("%s: %w", wholeValue.around(code), err)
	}
	return _Text{whole: &compiled}, nil
}

func (c *_Compiler) compileInterpolations(written string) (_Text, error) {
	var interpolation = c.delimiters.interpolation
	var compiled _Text
	var search = searchingEndsIn(written)
	var rest = written
	for {
		var start = strings.Index(rest, interpolation.opens)
		if start < 0 {
			return compiled.withLiteral(rest), nil
		}
		compiled = compiled.withLiteral(rest[:start])
		var after = rest[start+len(interpolation.opens):]
		end, err := search.expressionEnd(after, interpolation)
		if err != nil {
			return _Text{}, fmt.Errorf("%s%s: %w", interpolation.opens, excerpt(after, codeExcerptLength), err)
		}
		code, err := compileExpression(after[:end], interpolation)
		if err != nil {
			return _Text{}, fmt.Errorf("%s: %w", interpolation.around(after[:end]), err)
		}
		compiled.segments = append(compiled.segments, _Segment{code: &code})
		rest = after[end+len(interpolation.closes):]
	}
}

func (t _Text) withLiteral(literal string) _Text {
	if literal == "" {
		return t
	}
	t.segments = append(t.segments, _Segment{literal: literal})
	return t
}
