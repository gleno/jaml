package jaml

import (
	"context"
	"fmt"
	"strconv"
)

type _Servable interface {
	prepare(request _Work, host _Host) (_Prepared, error)
	outline() *_CodeOutline
}

type _Host struct {
	runtime _Runtime
	scripts *_ScriptCache
}

type _Prepared struct {
	spot   *_Spot
	render func(ctx context.Context) (_Rendered, error)
}

type _Rendered struct {
	tree    any
	path    *_DataPath
	skipped [][]string
}

type _Document struct {
	root     any
	compiled *_Compiled
	settings _ExpressionSettings
}

type _Subtree struct {
	node any
	path *_DataPath
	root any
}

func makeDocument(ctx context.Context, written string, configured _ExpressionSettings) (*_Document, error) {
	root, err := parseYAML(ctx, written)
	if err != nil {
		return nil, err
	}
	compiled, err := compileTexts(ctx, root, delimitersFor(configured.sigil))
	if err != nil {
		return nil, err
	}
	return &_Document{root: root, compiled: compiled, settings: configured}, nil
}

func compileRaw(source string, configured _ExpressionSettings) (*_Document, error) {
	compiled, err := compileBare(source, configured)
	if err != nil {
		return nil, _Unplaced{kind: invalidExpression, label: bare.around(source), cause: err}
	}
	return &_Document{root: source, compiled: &_Compiled{texts: map[string]_Text{source: {whole: &compiled}}}, settings: configured}, nil
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

func (d *_Document) prepare(request _Work, host _Host) (_Prepared, error) {
	chosen, err := d.selectPath(request.Path)
	if err != nil {
		return _Prepared{}, err
	}
	scripts, err := host.scripts.compileAll(request.Scripts)
	if err != nil {
		return _Prepared{}, err
	}
	var plan = _SessionPlan{values: request.Values, scripts: scripts, pinned: request.Pinned, runtime: host.runtime}
	var spot = &_Spot{selection: chosen.path}
	return _Prepared{spot: spot, render: func(ctx context.Context) (_Rendered, error) {
		var engine = &_LazySession{ctx: ctx, plan: plan}
		defer engine.close()
		if err := engine.warm(); err != nil {
			return _Rendered{}, err
		}
		var renderer = &_Renderer{compiled: d.compiled, settings: d.settings, engine: engine, spot: spot, args: request.Args, skips: request.Skips, depth: chosen.path.depth()}
		tree, err := renderer.render(chosen.node, renderer.place(chosen))
		if err != nil {
			return _Rendered{}, err
		}
		return _Rendered{tree: tree, path: chosen.path, skipped: renderer.skipped}, nil
	}}, nil
}

func (d *_Document) outline() *_CodeOutline {
	return d.compiled.outline(d.root)
}

func encodeResult(result _Rendered) ([]byte, error) {
	var encoded = unboundedWriter()
	if err := encoded.data(result.tree); err != nil {
		return nil, failWith(coercionFailed, err).at(result.path.String())
	}
	return encoded.written, nil
}

func (d *_Document) selectPath(path []string) (_Subtree, error) {
	current, location, root := d.root, (*_DataPath)(nil), any(nil)
	for _, step := range path {
		if root == nil {
			root = d.compiled.staticRoot(current)
		}
		switch typed := current.(type) {
		case *_Mapping:
			found, ok := typed.values[step]
			if !ok {
				return _Subtree{}, noSuchPath(location, fmt.Sprintf("has no key %q", step))
			}
			current, location = found, location.atStep(step)
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || strconv.Itoa(index) != step || index < 0 || index >= len(typed) {
				return _Subtree{}, noSuchPath(location, fmt.Sprintf("is a list of %d, which has no item %q", len(typed), step))
			}
			current, location = typed[index], location.atStep(step)
		default:
			return _Subtree{}, d.missingInside(_Subtree{node: current, path: location}, step)
		}
	}
	return _Subtree{node: current, path: location, root: root}, nil
}

func (d *_Document) missingInside(leaf _Subtree, step string) error {
	if written, isString := leaf.node.(string); isString && d.compiled.holdsCode(written) {
		return noSuchPath(leaf.path, fmt.Sprintf("is code; At cannot look inside what it produces: resolve %s and index the result", leaf.path))
	}
	return noSuchPath(leaf.path, fmt.Sprintf("is %s, which has no key %q", describeLeaf(leaf.node), step))
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

func noSuchPath(path *_DataPath, message string) error {
	return fail(missingPath, "%s", message).at(path.String())
}

func eachChild(node any, path *_DataPath, visit func(child any, path *_DataPath) error) error {
	switch typed := node.(type) {
	case *_Mapping:
		for _, key := range typed.keys {
			if err := visit(typed.values[key], path.atKey(key)); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := visit(item, path.atIndex(index)); err != nil {
				return err
			}
		}
	}
	return nil
}
