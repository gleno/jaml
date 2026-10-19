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
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, nestedTemplate)
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("config", "servers", "0", "port")), 8001)
		expectEqual(t, mustResolve[string](t, t.Context(), document.At("config", "servers", "1", "name")), "b")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("config", "dotted.key")), 1)
	})
}

func TestNoPathDecodesTheWholeDocument(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a: 1\n")
		expectEqual(t, evalAs[map[string]int](t, document, nil), map[string]int{"a": 1})
	})
}

func TestPathDoesNotEvaluateSiblings(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "good: 1\nbad: ${ (() => { throw new Error('x') })() }\n")
		expectEqual(t, mustResolve[int](t, t.Context(), document.At("good")), 1)
	})
}

func TestAMissingPathIsAnErrorInBothKinds(t *testing.T) {
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
		"into an expression": {[]string{"config", "computed", "inner"}, []string{"$.config.computed is code; At cannot look inside what it produces: resolve $.config.computed and index the result"}},
		"into interpolation": {[]string{"config", "label", "x"}, []string{"$.config.label is code; At cannot look inside"}},
		"into an evaluated":  {[]string{"config", "servers", "0", "port", "x"}, []string{"$.config.servers[0].port is code; At cannot look inside"}},
		"into a null":        {[]string{"config", "empty", "x"}, []string{"$.config.empty", "is a null"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			agreement{text: nestedTemplate, path: c.path}.eval(t, intoAny).expectRefused(t, MissingPath, c.fragments...)
		})
	}
}

func TestPathIntoAnEmptyDocumentIsMissing(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("a")), MissingPath, "is a null")
	})
}

func TestKeysWithSpecialCharactersAreQuotedInPaths(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "\"a b\":\n  \"x.y\": ${missing}\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document), EvaluationFailed, `$["a b"]["x.y"]`)
	})
}

func allocatedBy(run func()) uint64 {
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	run()
	goruntime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestAFailureUnderLongAliasedKeysCostsNothingUntilItIsReported(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var text strings.Builder
		text.WriteString("anchor: &k " + strings.Repeat("k", 100_000) + "\nnest:\n")
		for level := 1; level <= 200; level++ {
			text.WriteString(strings.Repeat("  ", level) + "*k :\n")
		}
		text.WriteString(strings.Repeat("  ", 201) + "${ missing }\n")
		document := mustJaml(t, sb, text.String())
		var err error
		allocated := allocatedBy(func() {
			var out any
			err = resolveInto(t.Context(), &out, document)
		})
		expectError(t, err, EvaluationFailed, "$.nest.kkk", "…", "ReferenceError")
		if allocated > 64<<20 {
			t.Fatalf("failing the evaluation allocated %d bytes", allocated)
		}
		if len(err.Error()) > 64<<10 {
			t.Fatalf("the error is %d bytes long", len(err.Error()))
		}
	})
}

func TestARefusalUnderLongResultKeysCostsNothingUntilItIsReported(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "v: \"${ (() => { const k = 'k'.repeat(1 << 20); let o = Symbol(); for (let i = 0; i < 40; i++) o = {[k]: o}; return o })() }\"\n")
		var err error
		allocated := allocatedBy(func() {
			var out any
			err = resolveInto(t.Context(), &out, document)
		})
		expectError(t, err, EvaluationFailed, "$.v: ", "result.kkk", "…", "is a symbol")
		if allocated > 128<<20 {
			t.Fatalf("refusing the result allocated %d bytes", allocated)
		}
		if len(err.Error()) > 64<<10 {
			t.Fatalf("the error is %d bytes long", len(err.Error()))
		}
	})
}

func TestLongKeysAreExcerptedInPaths(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
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
				document := mustJaml(t, sb, "\""+c.key+"\":\n  v: ${ missing }\n")
				var out any
				expectError(t, resolveInto(t.Context(), &out, document), EvaluationFailed, c.spelled+": ${missing}")
			})
		}
	})
}

func TestErrorsUnderASelectedPathKeepTheirFullPrefix(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		document := mustJaml(t, sb, "a:\n  - b:\n      \"c d\": ${ missing }\n")
		var out any
		expectError(t, resolveInto(t.Context(), &out, document.At("a", "0")), EvaluationFailed, `$.a[0].b["c d"]: ${missing}: ReferenceError`)
	})
}
