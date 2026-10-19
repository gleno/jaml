package jaml

import (
	"context"
	"fmt"
	"strconv"
)

type _Servable interface {
	prepare(request _Work) (_Prepared, error)
}

type _Prepared func(ctx context.Context) (_Rendered, error)

type _Rendered struct {
	tree    any
	at      *_DataPath
	skipped [][]string
}

type _Document struct {
	root     any
	texts    map[string]_Text
	settings _ExpressionSettings
}

type _Subtree struct {
	node any
	at   *_DataPath
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
	return &_Document{root: source, texts: map[string]_Text{source: {whole: &compiled}}, settings: configured}, nil
}

func compileBare(source string, configured _ExpressionSettings) (_Expression, error) {
	parsed, err := parseCode(source)
	if err != nil {
		return _Expression{}, err
	}
	if configured.function {
		if err := parsed.refuseUnlessFunction(); err != nil {
			return _Expression{}, err
		}
	}
	return parsed.compile(bare)
}

func (d *_Document) prepare(request _Work) (_Prepared, error) {
	chosen, err := d.selectPath(request.Path)
	if err != nil {
		return nil, err
	}
	var plan = _SessionPlan{values: request.Values, scripts: request.Scripts, pinned: request.Pinned}
	return func(ctx context.Context) (_Rendered, error) {
		var engine = &_LazySession{ctx: ctx, plan: plan}
		defer engine.close()
		if err := engine.warm(); err != nil {
			return _Rendered{}, err
		}
		var walk = &_Renderer{texts: d.texts, settings: d.settings, engine: engine, args: request.Args, skips: request.Skips, depth: chosen.at.depth()}
		tree, err := walk.render(chosen.node, walk.place(chosen.at))
		if err != nil {
			return _Rendered{}, err
		}
		return _Rendered{tree: tree, at: chosen.at, skipped: walk.skipped}, nil
	}, nil
}

func encodeResult(result _Rendered) ([]byte, error) {
	var encoded = unboundedWriter()
	if err := encoded.data(result.tree); err != nil {
		return nil, fail(CoercionFailed, "%w", err).within(result.at.String())
	}
	return encoded.written, nil
}

func (d *_Document) selectPath(path []string) (_Subtree, error) {
	current, at := d.root, (*_DataPath)(nil)
	for _, step := range path {
		switch typed := current.(type) {
		case *_Mapping:
			found, ok := typed.values[step]
			if !ok {
				return _Subtree{}, missingPath(at, fmt.Sprintf("has no key %q", step))
			}
			current, at = found, at.atStep(step)
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || strconv.Itoa(index) != step || index < 0 || index >= len(typed) {
				return _Subtree{}, missingPath(at, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, at = typed[index], at.atStep(step)
		default:
			return _Subtree{}, d.missingInside(_Subtree{node: current, at: at}, step)
		}
	}
	return _Subtree{node: current, at: at}, nil
}

func (d *_Document) missingInside(leaf _Subtree, step string) error {
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

func missingPath(at *_DataPath, message string) error {
	return fail(MissingPath, "%s", message).within(at.String())
}

func eachChild(node any, at *_DataPath, visit func(child any, at *_DataPath) error) error {
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
