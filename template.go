package jaml

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/gleno/fault"
)

type template struct {
	root     any
	texts    map[string]text
	sources  []source
	settings settings
}

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
	texts, sources, err := compileTexts(root)
	if err != nil {
		return nil, err
	}
	return &template{root: root, texts: texts, sources: sources, settings: configured}, nil
}

func (t *template) eval(runs engine, call evalCall) error {
	target := reflect.ValueOf(call.into)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fault.From(InvalidTarget, fmt.Sprintf("Eval needs a non-nil pointer, got %s", describeTarget(call.into)))
	}
	selected, at, err := t.selectPath(call.path)
	if err != nil {
		return err
	}
	renderer := &renderer{template: t, engine: runs, vars: call.vars}
	defer renderer.close()
	rendered, err := renderer.render(selected, at)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(rendered)
	if err != nil {
		return fault.From(CoercionFailed, fmt.Sprintf("%s: %s", at, err))
	}
	if err := json.Unmarshal(encoded, call.into); err != nil {
		return fault.From(CoercionFailed, fmt.Sprintf("%s: %s", at, err))
	}
	return nil
}
