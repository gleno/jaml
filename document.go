package jaml

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
)

type servable interface {
	prepare(request work) (prepared, error)
}

type prepared func(ctx context.Context) (answered, error)

type _Document struct {
	root     any
	texts    map[string]text
	settings _ExpressionSettings
}

type subtree struct {
	node any
	at   *dataPath
}

func makeDocument(ctx context.Context, written string, configured _ExpressionSettings) (*_Document, error) {
	root, err := parseYAML(ctx, written)
	if err != nil {
		return nil, err
	}
	texts, err := compileTexts(ctx, root, delimitersFor(configured.sigil))
	if err != nil {
		return nil, err
	}
	return &_Document{root: root, texts: texts, settings: configured}, nil
}

func compileRaw(source string, configured _ExpressionSettings) (*_Document, error) {
	compiled, err := compileBare(source, configured)
	if err != nil {
		return nil, fail(InvalidExpression, "%s: %w", bare.around(source), err)
	}
	return &_Document{root: source, texts: map[string]text{source: {whole: &compiled}}, settings: configured}, nil
}

func compileBare(source string, configured _ExpressionSettings) (expression, error) {
	parsed, err := parseCode(source)
	if err != nil {
		return expression{}, err
	}
	if configured.function {
		if err := parsed.refuseUnlessFunction(); err != nil {
			return expression{}, err
		}
	}
	return parsed.compile(bare)
}

func (d *_Document) prepare(request work) (prepared, error) {
	chosen, err := d.selectPath(request.Path)
	if err != nil {
		return nil, err
	}
	var plan = sessionPlan{values: request.Values, scripts: request.Scripts, pinned: request.Pinned}
	return func(ctx context.Context) (answered, error) {
		var engine = &lazySession{ctx: ctx, plan: plan}
		defer engine.close()
		if err := engine.warm(); err != nil {
			return answered{}, err
		}
		var walk = &renderer{texts: d.texts, settings: d.settings, engine: engine, args: request.Args, skips: request.Skips, depth: chosen.at.depth()}
		tree, err := walk.render(chosen.node, walk.place(chosen.at))
		if err != nil {
			return answered{}, err
		}
		data, err := encodeResult(tree, chosen.at)
		return answered{data: data, skipped: walk.skipped}, err
	}, nil
}

func encodeResult(tree any, at *dataPath) ([]byte, error) {
	var encoded bytes.Buffer
	if err := writeTree(&encoded, tree); err != nil {
		return nil, fail(CoercionFailed, "%w", err).within(at.String())
	}
	return encoded.Bytes(), nil
}

func writeTree(out *bytes.Buffer, node any) error {
	switch typed := node.(type) {
	case *_Mapping:
		out.WriteByte('{')
		for index, key := range typed.keys {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeTree(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeTree(out, typed.values[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeTree(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		encoded, err := wireJSON(node)
		if err != nil {
			return err
		}
		out.Write(encoded)
	}
	return nil
}

func (d *_Document) selectPath(path []string) (subtree, error) {
	current, at := d.root, (*dataPath)(nil)
	for _, step := range path {
		switch typed := current.(type) {
		case *_Mapping:
			found, ok := typed.values[step]
			if !ok {
				return subtree{}, missingPath(at, fmt.Sprintf("has no key %q", step))
			}
			current, at = found, at.atStep(step)
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || strconv.Itoa(index) != step || index < 0 || index >= len(typed) {
				return subtree{}, missingPath(at, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, at = typed[index], at.atStep(step)
		default:
			return subtree{}, d.missingInside(subtree{node: current, at: at}, step)
		}
	}
	return subtree{node: current, at: at}, nil
}

func (d *_Document) missingInside(leaf subtree, step string) error {
	if written, isString := leaf.node.(string); isString && d.texts[written].holdsCode() {
		return missingPath(leaf.at, fmt.Sprintf("is code; At cannot look inside what it produces: resolve %s and index the result", leaf.at))
	}
	return missingPath(leaf.at, fmt.Sprintf("is %s, which has no key %q", describeLeaf(leaf.node), step))
}

func describeLeaf(node any) string {
	switch node.(type) {
	case nil:
		return "a null"
	case string:
		return "a string"
	case bool:
		return "a bool"
	default:
		return "a number"
	}
}

func missingPath(at *dataPath, message string) error {
	return fail(MissingPath, "%s", message).within(at.String())
}

func eachChild(node any, at *dataPath, visit func(child any, at *dataPath) error) error {
	switch typed := node.(type) {
	case *_Mapping:
		for _, key := range typed.keys {
			if err := visit(typed.values[key], at.atKey(key)); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := visit(item, at.atIndex(index)); err != nil {
				return err
			}
		}
	}
	return nil
}
