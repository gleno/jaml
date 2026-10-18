package jaml

import "github.com/gleno/fault"

type TrustedTemplate struct {
	core *template
}

func MakeTrustedTemplate(written string, options ...Option) (*TrustedTemplate, error) {
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	if configured.memoryLimit != 0 {
		return nil, fault.From(InvalidTemplate, "a trusted template has no memory limit; use MakeSecureTemplate")
	}
	core, err := makeTemplate(written, configured)
	if err != nil {
		return nil, err
	}
	return &TrustedTemplate{core: core}, nil
}

func (t *TrustedTemplate) Eval(into any, vars map[string]any, path ...string) error {
	return t.template().eval(inProcess{}, evalCall{into: into, vars: vars, path: path})
}

func (t *TrustedTemplate) template() *template {
	if t == nil {
		return nil
	}
	return t.core
}
