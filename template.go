package jaml

import (
	"context"
	"encoding/json"
	"errors"
)

var errNotCallable = errors.New("a template cannot be called; compile an expression")

type servable interface {
	prepare(request work) (prepared, error)
}

type prepared struct {
	at  string
	run func(ctx context.Context) ([]byte, error)
}

type template struct {
	root     any
	texts    map[string]text
	settings settings
}

func makeTemplate(written string, configured settings) (*template, error) {
	root, err := parseYAML(written)
	if err != nil {
		return nil, err
	}
	texts, err := compileTexts(root)
	if err != nil {
		return nil, err
	}
	return &template{root: root, texts: texts, settings: configured}, nil
}

func (t *template) prepare(request work) (prepared, error) {
	if request.Call != nil {
		return prepared{}, fail(EvaluationFailed, "%w", errNotCallable)
	}
	chosen, err := t.selectPath(request.Path)
	if err != nil {
		return prepared{}, err
	}
	plan := sessionPlan{vars: request.Vars, pinned: request.Pinned}
	return prepared{at: chosen.at.String(), run: func(ctx context.Context) ([]byte, error) {
		engine := &lazySession{ctx: ctx, plan: plan}
		defer engine.close()
		return t.renderSubtree(chosen, engine)
	}}, nil
}

func (t *template) renderSubtree(chosen subtree, engine *lazySession) ([]byte, error) {
	renderer := &renderer{texts: t.texts, settings: t.settings, engine: engine}
	tree, err := renderer.render(chosen.node, chosen.at)
	if err != nil {
		return nil, err
	}
	return encodeResult(tree, chosen.at)
}

func encodeResult(tree any, at *dataPath) ([]byte, error) {
	encoded, err := json.Marshal(tree)
	if err != nil {
		return nil, fail(CoercionFailed, "%s: %w", at, err)
	}
	return encoded, nil
}
