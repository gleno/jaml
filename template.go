package jaml

import (
	"encoding/json"
	"reflect"
)

type template struct {
	root     any
	texts    map[string]text
	settings settings
}

type renderSite func(job rendering) (string, []byte, error)

type evalCall struct {
	into any
	vars map[string]any
	path []string
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

func evalThrough(renders renderSite, call evalCall) error {
	target := reflect.ValueOf(call.into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fail(InvalidTarget, "Eval needs a non-nil pointer, got %s", describeTarget(call.into))
	}
	vars, err := encodeVars(call.vars)
	if err != nil {
		return fail(EvaluationFailed, "binding vars: %w", err)
	}
	at, rendered, err := renders(rendering{Path: call.path, Vars: vars})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rendered, call.into); err != nil {
		return fail(CoercionFailed, "%s: %w", blame(at, rendered, err), err)
	}
	return nil
}

func (t *template) render(job rendering) (string, []byte, error) {
	chosen, err := t.selectPath(job.Path)
	if err != nil {
		return "", nil, err
	}
	rendered, err := t.renderSubtree(chosen, job.Vars)
	return chosen.at.String(), rendered, err
}

func (t *template) renderSubtree(chosen subtree, vars encodedVars) ([]byte, error) {
	renderer := &renderer{template: t, engine: &lazySession{vars: vars}}
	tree, err := renderer.render(chosen.node, chosen.at)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		return nil, fail(CoercionFailed, "%s: %w", chosen.at, err)
	}
	return encoded, nil
}

func unmadeTemplate() error {
	return fail(InvalidTemplate, "Eval on a template not made by MakeTrustedTemplate or MakeSecureTemplate")
}
