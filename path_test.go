package jaml

import (
	goruntime "runtime"
	"strings"
	"testing"
)

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

func TestAMissingPathIsAnErrorInBothModes(t *testing.T) {
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
			agreement{text: nestedTemplate, path: c.path}.eval(t, intoAny).expectRefused(t, MissingPath, c.fragments...)
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

func allocatedBy(run func()) uint64 {
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	run()
	goruntime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestAFailureUnderLongAliasedKeysCostsNothingUntilItIsReported(t *testing.T) {
	var text strings.Builder
	text.WriteString("anchor: &k " + strings.Repeat("k", 100_000) + "\nnest:\n")
	for level := 1; level <= 200; level++ {
		text.WriteString(strings.Repeat("  ", level) + "*k :\n")
	}
	text.WriteString(strings.Repeat("  ", 201) + "${ missing }\n")
	template := mustTemplate(t, text.String())
	var err error
	allocated := allocatedBy(func() {
		var out any
		err = template.Eval(&out, nil)
	})
	expectError(t, err, EvaluationFailed, "$.nest.kkk", "…", "ReferenceError")
	if allocated > 64<<20 {
		t.Fatalf("failing the evaluation allocated %d bytes", allocated)
	}
	if len(err.Error()) > 64<<10 {
		t.Fatalf("the error is %d bytes long", len(err.Error()))
	}
}

func TestARefusalUnderLongResultKeysCostsNothingUntilItIsReported(t *testing.T) {
	template := mustTemplate(t, "v: \"${ (() => { const k = 'k'.repeat(1 << 20); let o = Symbol(); for (let i = 0; i < 40; i++) o = {[k]: o}; return o })() }\"\n")
	var err error
	allocated := allocatedBy(func() {
		var out any
		err = template.Eval(&out, nil)
	})
	expectError(t, err, EvaluationFailed, "$.v: ", "result.kkk", "…", "is a symbol")
	if allocated > 128<<20 {
		t.Fatalf("refusing the result allocated %d bytes", allocated)
	}
	if len(err.Error()) > 64<<10 {
		t.Fatalf("the error is %d bytes long", len(err.Error()))
	}
}

func TestLongKeysAreExcerptedInPaths(t *testing.T) {
	sixty, sixtyOne := strings.Repeat("k", 60), strings.Repeat("k", 61)
	cases := map[string]struct {
		key, spelled string
	}{
		"plain at the limit": {sixty, "$." + sixty + ".v"},
		"plain over it":      {sixtyOne, "$." + sixty + "….v"},
		"quoted over it":     {"a b" + strings.Repeat("é", 60), `$["a b` + strings.Repeat("é", 57) + `…"].v`},
		"plain judged whole": {sixty + "!", `$["` + sixty + `…"].v`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			template := mustTemplate(t, "\""+c.key+"\":\n  v: ${ missing }\n")
			var out any
			expectError(t, template.Eval(&out, nil), EvaluationFailed, c.spelled+": ${missing}")
		})
	}
}

func TestErrorsUnderASelectedPathKeepTheirFullPrefix(t *testing.T) {
	template := mustTemplate(t, "a:\n  - b:\n      \"c d\": ${ missing }\n")
	var out any
	expectError(t, template.Eval(&out, nil, "a", "0"), EvaluationFailed, `$.a[0].b["c d"]: ${missing}: ReferenceError`)
}
