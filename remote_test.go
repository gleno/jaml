package jaml

import (
	"bufio"
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func countRequests(t *testing.T, template *SecureTemplate) *atomic.Int64 {
	t.Helper()
	slot := <-template.slot
	defer func() { template.slot <- slot }()
	forwarded := slot.process.requests
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	slot.process.requests = write
	t.Cleanup(func() { closeAll(read, write) })
	counted := &atomic.Int64{}
	go func() {
		lines := bufio.NewReader(read)
		for {
			line, err := lines.ReadBytes('\n')
			if err != nil {
				return
			}
			counted.Add(1)
			if _, err := forwarded.Write(line); err != nil {
				return
			}
		}
	}()
	return counted
}

func TestASecureEvalIsOneExchange(t *testing.T) {
	template := mustSecure(t, "a: ${ n }\nb: \"${{ n + 1 }} and ${{ n + 2 }}\"\nc: [\"${ n * 2 }\", plain]\n")
	requests := countRequests(t, template)
	for n := range 3 {
		got := secureEvalAs[map[string]any](t, template, map[string]any{"n": n})
		expectEqual(t, got["a"], float64(n))
	}
	expectEqual(t, requests.Load(), int64(3))
}

type agreement struct {
	text    string
	vars    map[string]any
	path    []string
	options []Option
}

func (a agreement) eval(t *testing.T, newOut func() any) bothModes {
	t.Helper()
	trustedOut, secureOut := newOut(), newOut()
	trusted := mustTemplate(t, a.text, a.options...).Eval(trustedOut, a.vars, a.path...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secure := mustSecure(t, a.text, a.options...).Eval(ctx, secureOut, a.vars, a.path...)
	if trusted == nil && secure == nil {
		expectEqual(t, secureOut, trustedOut)
	}
	return bothModes{trusted: trusted, secure: secure}
}

func intoAny() any { return new(any) }

func TestNumbersAgreeInBothModes(t *testing.T) {
	text := "v: \"${ [1e21, 2 ** 53 + 1, -0, 0.1, 1 / 3, -7, 123456789012] }\"\nt: \"${{ 2 ** 53 + 1 }} ${{ 0.1 + 0.2 }}\"\n"
	agreement{text: text}.eval(t, intoAny).expectAccepted(t)
	agreement{text: text, path: []string{"v"}}.eval(t, func() any { return new([]int64) }).expectRefused(t, CoercionFailed, "$.v")
}

func TestAbsenceAgreesInBothModes(t *testing.T) {
	text := "a: ${ undefined }\nb: ${ null }\nc: \"[${{ undefined }}|${{ null }}]\"\n"
	agreement{text: text, options: []Option{WithUndefinedCoercion(Loose)}}.eval(t, intoAny).expectAccepted(t)
	agreement{text: text}.eval(t, intoAny).expectRefused(t, CoercionFailed, "$.a", "strict undefined")
	agreement{text: text, path: []string{"b"}, options: []Option{WithNullCoercion(Strict)}}.eval(t, intoAny).expectRefused(t, CoercionFailed, "$.b", "strict null")
}

func TestFailuresAgreeInBothModes(t *testing.T) {
	cases := map[string]struct {
		agreement agreement
		sentinel  error
	}{
		"empty thrown message": {agreement{text: "v: \"${ (() => { throw { message: '' } })() }\"\n"}, EvaluationFailed},
		"thrown error":         {agreement{text: "v: ${ (() => { throw new TypeError('t') })() }\n"}, EvaluationFailed},
		"missing var":          {agreement{text: "v: ${ nope }\n"}, EvaluationFailed},
		"not data":             {agreement{text: "v: ${ () => 1 }\n"}, EvaluationFailed},
		"missing path":         {agreement{text: "v: ${ 1 }\n", path: []string{"w"}}, MissingPath},
		"path into code":       {agreement{text: "v: \"${ ({a: 1}) }\"\n", path: []string{"v", "a"}}, MissingPath},
		"immutable global var": {agreement{text: "v: ${ 1 }\n", vars: map[string]any{"undefined": 1}}, EvaluationFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.agreement.eval(t, intoAny).expectRefused(t, c.sentinel)
		})
	}
}

func TestADeepResultInADeepTemplateAgreesInBothModes(t *testing.T) {
	agreement{text: deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`)}.eval(t, intoAny).expectAccepted(t)
	agreement{text: deepTemplate(4096, `"${ `+nestedArrayExpression(4096)+` }"`), path: []string{"v"}}.eval(t, func() any { return new([]any) }).expectAccepted(t)
}
