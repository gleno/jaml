package jaml

import (
	"testing"
	"time"
)

type _Agreement struct {
	text    string
	vars    map[string]any
	path    []string
	options []Option
}

func (a _Agreement) eval(t *testing.T, newOut func() any) _BothKinds {
	t.Helper()
	trustedOut, isolatedOut := newOut(), newOut()
	var trusted = mustJaml(t, sharedSandbox(t, trustedKind), a.text, a.options...).At(a.path...).resolve(valued(within(t, 30*time.Second), a.vars), trustedOut, nil)
	var isolated = mustJaml(t, sharedSandbox(t, unprotectedKind), a.text, a.options...).At(a.path...).resolve(valued(within(t, 30*time.Second), a.vars), isolatedOut, nil)
	if trusted == nil && isolated == nil {
		expectEqual(t, isolatedOut, trustedOut)
	}
	return _BothKinds{trusted: trusted, isolated: isolated}
}

func intoAny() any { return new(any) }

func TestNumbersAgreeInBothKinds(t *testing.T) {
	var text = "v: \"${ [1e21, 2 ** 53 + 1, -0, 0.1, 1 / 3, -7, 123456789012] }\"\nt: \"${{ 2 ** 53 + 1 }} ${{ 0.1 + 0.2 }}\"\n"
	_Agreement{text: text}.eval(t, intoAny).expectAccepted(t)
	_Agreement{text: text, path: []string{"v"}}.eval(t, func() any { return new([]int64) }).expectRefused(t, CoercionFailed, "$.v")
}

func TestAbsenceAgreesInBothKinds(t *testing.T) {
	var text = "a: ${ undefined }\nb: ${ null }\nc: \"[${{ undefined }}|${{ null }}]\"\n"
	_Agreement{text: text, options: []Option{WithUndefinedCoercion(Loose)}}.eval(t, intoAny).expectAccepted(t)
	_Agreement{text: text}.eval(t, intoAny).expectRefused(t, CoercionFailed, "$.a", "strict undefined")
	_Agreement{text: text, path: []string{"b"}, options: []Option{WithNullCoercion(Strict)}}.eval(t, intoAny).expectRefused(t, CoercionFailed, "$.b", "strict null")
}

func TestFailuresAgreeInBothKinds(t *testing.T) {
	var cases = map[string]struct {
		agreement _Agreement
		sentinel  error
	}{
		"empty thrown message": {_Agreement{text: "v: \"${ (() => { throw { message: '' } })() }\"\n"}, EvaluationFailed},
		"thrown error":         {_Agreement{text: "v: ${ (() => { throw new TypeError('t') })() }\n"}, EvaluationFailed},
		"missing var":          {_Agreement{text: "v: ${ nope }\n"}, EvaluationFailed},
		"not data":             {_Agreement{text: "v: ${ [() => 1] }\n"}, EvaluationFailed},
		"missing path":         {_Agreement{text: "v: ${ 1 }\n", path: []string{"w"}}, MissingPath},
		"path into code":       {_Agreement{text: "v: \"${ ({a: 1}) }\"\n", path: []string{"v", "a"}}, MissingPath},
		"immutable global var": {_Agreement{text: "v: ${ 1 }\n", vars: map[string]any{"undefined": 1}}, EvaluationFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.agreement.eval(t, intoAny).expectRefused(t, c.sentinel)
		})
	}
}

func TestADeepResultInADeepTemplateAgreesInBothKinds(t *testing.T) {
	_Agreement{text: deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`)}.eval(t, intoAny).expectAccepted(t)
	_Agreement{text: deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`), path: []string{"v"}}.eval(t, func() any { return new([]any) }).expectAccepted(t)
}
