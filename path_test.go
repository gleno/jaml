package jaml

import "testing"

const nestedTemplate = `
config:
  servers:
    - name: a
      port: ${8000 + 1}
    - name: b
  "dotted.key": 1
  computed: "${ {inner: 1} }"
  empty: ~
  label: "x ${{1}}"
`

func TestPathSelectsASubtreeBeforeDecoding(t *testing.T) {
	template := mustTemplate(t, nestedTemplate)
	expectEqual(t, evalAs[int](t, template, nil, "config", "servers", "0", "port"), 8001)
	expectEqual(t, evalAs[string](t, template, nil, "config", "servers", "1", "name"), "b")
	expectEqual(t, evalAs[int](t, template, nil, "config", "dotted.key"), 1)
}

func TestNoPathDecodesTheWholeDocument(t *testing.T) {
	template := mustTemplate(t, "a: 1\n")
	expectEqual(t, evalAs[map[string]int](t, template, nil), map[string]int{"a": 1})
}

func TestPathDoesNotEvaluateSiblings(t *testing.T) {
	template := mustTemplate(t, "good: 1\nbad: ${ (() => { throw new Error('x') })() }\n")
	expectEqual(t, evalAs[int](t, template, nil, "good"), 1)
}

func TestAMissingPathIsAnError(t *testing.T) {
	template := mustTemplate(t, nestedTemplate)
	cases := map[string]struct {
		path      []string
		fragments []string
	}{
		"missing key":        {[]string{"nope"}, []string{`$ has no key "nope"`}},
		"missing nested key": {[]string{"config", "nope"}, []string{`$.config has no key "nope"`}},
		"index out of range": {[]string{"config", "servers", "2"}, []string{"$.config.servers is a list of 2", `no item "2"`}},
		"index not a number": {[]string{"config", "servers", "first"}, []string{`no item "first"`}},
		"negative index":     {[]string{"config", "servers", "-1"}, []string{`no item "-1"`}},
		"into a scalar":      {[]string{"config", "servers", "1", "name", "x"}, []string{"$.config.servers[1].name", `is a string, which has no key "x"`}},
		"into an expression": {[]string{"config", "computed", "inner"}, []string{"$.config.computed", "is an expression, unevaluated until Eval"}},
		"into interpolation": {[]string{"config", "label", "x"}, []string{"is an expression"}},
		"into an evaluated":  {[]string{"config", "servers", "0", "port", "x"}, []string{"is an expression"}},
		"into a null":        {[]string{"config", "empty", "x"}, []string{"$.config.empty", "is a null"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var out any
			expectError(t, template.Eval(&out, nil, c.path...), MissingPath, c.fragments...)
		})
	}
}

func TestPathIntoAnEmptyDocumentIsMissing(t *testing.T) {
	template := mustTemplate(t, "")
	var out any
	expectError(t, template.Eval(&out, nil, "a"), MissingPath, "is a null")
}

func TestKeysWithSpecialCharactersAreQuotedInPaths(t *testing.T) {
	template := mustTemplate(t, "\"a b\":\n  \"x.y\": ${missing}\n")
	var out any
	expectError(t, template.Eval(&out, nil), EvaluationFailed, `$["a b"]["x.y"]`)
}
