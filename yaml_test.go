package jaml

import (
	"math/big"
	"testing"
)

func TestYAMLFollowsSigsYAMLOneOneRules(t *testing.T) {
	template := mustTemplate(t, "plain: y\nquoted: \"y\"\nn: key\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"plain": true, "quoted": "y", "false": "key"})
}

func TestIntegersBeyondFloat64LoseDigitsUnlessQuoted(t *testing.T) {
	var out big.Int
	expectError(t, mustTemplate(t, "v: 123456789012345678901234567890\n").Eval(&out, nil, "v"), CoercionFailed, "1.2345678901234568e+29")
	quoted := evalAs[string](t, mustTemplate(t, "v: \"123456789012345678901234567890\"\n"), nil, "v")
	expectEqual(t, quoted, "123456789012345678901234567890")
}

func TestIntegersUpToInt64KeepTheirDigits(t *testing.T) {
	expectEqual(t, evalAs[int64](t, mustTemplate(t, "v: 9007199254740993\n"), nil, "v"), int64(9007199254740993))
}

func TestAnEmptyDocumentIsNull(t *testing.T) {
	out := 5
	if err := mustTemplate(t, "").Eval(&out, nil); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, out, 5)
}

func TestInvalidYAMLFailsAtMakeTemplate(t *testing.T) {
	_, err := MakeTemplate("a: [1, 2\n")
	expectError(t, err, InvalidTemplate)
}

func TestAliasesExpandWithTheirExpressions(t *testing.T) {
	template := mustTemplate(t, "base: &b {count: \"${ count }\"}\ncopy: *b\n")
	got := evalAs[map[string]map[string]int](t, template, map[string]any{"count": 4})
	expectEqual(t, got, map[string]map[string]int{"base": {"count": 4}, "copy": {"count": 4}})
}

func TestCommentsDoNotLeakIntoValues(t *testing.T) {
	template := mustTemplate(t, "a: ${1+1} # two\nb: \"# not a comment\"\n")
	got := evalAs[map[string]any](t, template, nil)
	expectEqual(t, got, map[string]any{"a": float64(2), "b": "# not a comment"})
}
