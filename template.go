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

type renderSite func(job rendering) ([]byte, error)

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

func (t *template) eval(renders renderSite, call evalCall) error {
	if t == nil {
		return unmadeTemplate()
	}
	target := reflect.ValueOf(call.into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fail(InvalidTarget, "Eval needs a non-nil pointer, got %s", describeTarget(call.into))
	}
	vars, err := encodeVars(call.vars)
	if err != nil {
		return fail(EvaluationFailed, "binding vars: %w", err)
	}
	_, at, err := t.selectPath(call.path)
	if err != nil {
		return err
	}
	rendered, err := renders(rendering{Path: call.path, Vars: vars})
	if err != nil {
		return located(at, err)
	}
	if err := json.Unmarshal(rendered, call.into); err != nil {
		return fail(CoercionFailed, "%s: %w", at.blame(rendered, err), err)
	}
	return nil
}

func (t *template) render(job rendering) ([]byte, error) {
	selected, at, err := t.selectPath(job.Path)
	if err != nil {
		return nil, err
	}
	renderer := &renderer{template: t, engine: &lazySession{vars: job.Vars}}
	tree, err := renderer.render(selected, at)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		return nil, fail(CoercionFailed, "%s: %w", at, err)
	}
	return encoded, nil
}

func located(at *dataPath, err error) error {
	if _, isLocated := err.(templateError); isLocated {
		return err
	}
	return fail(EvaluationFailed, "%s: %w", at, err)
}

func unmadeTemplate() error {
	return fail(InvalidTemplate, "Eval on a template not made by MakeTrustedTemplate or MakeSecureTemplate")
}
