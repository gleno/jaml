package jaml

type TrustedTemplate struct {
	core *template
}

func MakeTrustedTemplate(written string, options ...Option) (*TrustedTemplate, error) {
	configured, err := configure(options)
	if err != nil {
		return nil, err
	}
	if configured.memoryLimit != 0 {
		return nil, fail(InvalidTemplate, "a trusted template has no memory limit; use MakeSecureTemplate")
	}
	core, err := makeTemplate(written, configured)
	if err != nil {
		return nil, err
	}
	return &TrustedTemplate{core: core}, nil
}

func (t *TrustedTemplate) Eval(into any, vars map[string]any, path ...string) error {
	if t == nil || t.core == nil {
		return unmadeTemplate()
	}
	return evalThrough(t.core.render, evalCall{into: into, vars: vars, path: path})
}
