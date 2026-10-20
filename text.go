package jaml

import (
	"context"
	"fmt"
	"maps"
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

func compileTexts(ctx context.Context, root any, delimiters _Delimiters) (*_Compiled, error) {
	var c = &_Compiler{ctx: ctx, texts: map[string]_Text{}, visited: map[any]bool{}, delimiters: delimiters}
	if _, err := c.visit(root, nil); err != nil {
		return nil, err
	}
	maps.DeleteFunc(c.visited, func(_ any, holds bool) bool { return !holds })
	return &_Compiled{texts: c.texts, coded: c.visited}, nil
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

func (c *_Compiler) visit(node any, path *_DataPath) (bool, error) {
	if written, isString := node.(string); isString {
		return c.visitText(written, path)
	}
	var identity = identityOf(node)
	if identity == nil {
		return false, nil
	}
	if holds, seen := c.visited[identity]; seen {
		return holds, nil
	}
	var holds = false
	var err = eachChild(node, path, func(child any, path *_DataPath) error {
		var childHolds, err = c.visit(child, path)
		holds = holds || childHolds
		return err
	})
	if err != nil {
		return false, err
	}
	c.visited[identity] = holds
	return holds, nil
}

func (c *_Compiler) visitText(written string, path *_DataPath) (bool, error) {
	if known, seen := c.texts[written]; seen {
		return known.holdsCode(), nil
	}
	if c.ctx.Err() != nil {
		return false, cancelled(c.ctx)
	}
	compiled, err := c.compileText(written)
	if err != nil && c.ctx.Err() != nil {
		return false, cancelled(c.ctx)
	}
	if err != nil {
		return false, failWith(invalidExpression, err).at(path.String())
	}
	if compiled.holdsCode() {
		c.texts[written] = compiled
	}
	return compiled.holdsCode(), nil
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
	end, err := codeEnd(after, wholeValue)
	if err != nil {
		return _Text{}, _LabelledCause{label: wholeValue.opens + excerpt(after, codeExcerptLength), cause: err}
	}
	if end != len(after)-len(wholeValue.closes) {
		return _Text{}, c.delimiters.notWhole()
	}
	var code = after[:end]
	compiled, err := compileExpression(code, wholeValue)
	if err != nil {
		return _Text{}, _LabelledCause{label: wholeValue.around(code), cause: err}
	}
	return _Text{whole: &compiled}, nil
}

func (c *_Compiler) compileInterpolations(written string) (_Text, error) {
	var interpolation = c.delimiters.interpolation
	var compiled _Text
	var rest = written
	for {
		var start = strings.Index(rest, interpolation.opens)
		if start < 0 {
			return compiled.withLiteral(rest), nil
		}
		compiled = compiled.withLiteral(rest[:start])
		var after = rest[start+len(interpolation.opens):]
		if c.ctx.Err() != nil {
			return _Text{}, cancelled(c.ctx)
		}
		end, err := codeEnd(after, interpolation)
		if err != nil {
			return _Text{}, _LabelledCause{label: interpolation.opens + excerpt(after, codeExcerptLength), cause: err}
		}
		code, err := compileExpression(after[:end], interpolation)
		if err != nil {
			return _Text{}, _LabelledCause{label: interpolation.around(after[:end]), cause: err}
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
