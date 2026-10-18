package jaml

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func coerce[T any](t *testing.T, source string) (T, error) {
	t.Helper()
	var out T
	template := mustTemplate(t, "v: "+source+"\n")
	err := template.Eval(&out, map[string]any{"text": "hi", "list": []int{1, 2}}, "v")
	return out, err
}

func expectCoerced[T any](t *testing.T, source string, want T) {
	t.Helper()
	got, err := coerce[T](t, source)
	if err != nil {
		t.Fatalf("%s: %v", source, err)
	}
	expectEqual(t, got, want)
}

func expectRefused[T any](t *testing.T, source string, fragments ...string) {
	t.Helper()
	_, err := coerce[T](t, source)
	expectError(t, err, CoercionFailed, fragments...)
}

func TestEverythingCoercesToString(t *testing.T) {
	cases := map[string]string{
		"plain":               "plain",
		"'quoted'":            "quoted",
		"123":                 "123",
		"1.50":                "1.50",
		"0x1F":                "0x1F",
		"0o17":                "0o17",
		"1e3":                 "1e3",
		"true":                "true",
		"TRUE":                "TRUE",
		"no":                  "no",
		"0644":                "0644",
		".inf":                ".inf",
		"2001-12-14":          "2001-12-14",
		"${42}":               "42",
		"${1.5}":              "1.5",
		"${true}":             "true",
		"${text}":             "hi",
		"[1, two, 3.0]":       `[1,"two",3]`,
		"{a: 1, b: [x]}":      `{"a":1,"b":["x"]}`,
		"${list}":             "[1,2]",
		"'${ {a: \"<&>\"} }'": `{"a":"<&>"}`,
		"{b: 2, a: 1}":        `{"b":2,"a":1}`,
	}
	for source, want := range cases {
		t.Run(source, func(t *testing.T) { expectCoerced(t, source, want) })
	}
}

func TestNumbersCoerceToIntegersWithoutLoss(t *testing.T) {
	expectCoerced(t, "42", 42)
	expectCoerced(t, "-42", int64(-42))
	expectCoerced(t, "+7", 7)
	expectCoerced(t, "0x1F", 31)
	expectCoerced(t, "0o17", 15)
	expectCoerced(t, "1e3", 1000)
	expectCoerced(t, "2.0", 2)
	expectCoerced(t, "${6*7}", 42)
	expectCoerced(t, "${2**53}", int64(9007199254740992))
	expectCoerced(t, "9007199254740993", int64(9007199254740993))
	expectCoerced(t, "9223372036854775807", int64(9223372036854775807))
	expectCoerced(t, "'42'", 42)
	expectCoerced(t, "${'-3'}", int8(-3))
	expectCoerced(t, "'1e2'", 100)
	expectRefused[int](t, "1.5", "cannot coerce number 1.5 into int", "fractional")
	expectRefused[int](t, "${0.1+0.2}", "fractional")
	expectRefused[int8](t, "128", "out of range")
	expectRefused[int64](t, "9223372036854775808", "out of range")
	expectRefused[int](t, "${1e300}", "out of range")
	expectRefused[int](t, "' 42'", "JSON number")
	expectRefused[int](t, "'0x10'", "JSON number")
	expectRefused[int](t, "'1_000'", "JSON number")
	expectRefused[int](t, "1_000", "JSON number")
	expectRefused[int](t, "abc", `cannot coerce string "abc" into int`)
	expectRefused[int](t, "true", "cannot coerce bool true into int")
	expectRefused[int](t, "[1]", "cannot coerce list into int")
	expectRefused[int](t, "{a: 1}", "cannot coerce object into int")
}

func TestNumbersCoerceToUnsignedIntegers(t *testing.T) {
	expectCoerced(t, "255", uint8(255))
	expectCoerced(t, "18446744073709551615", uint64(18446744073709551615))
	expectRefused[uint8](t, "256", "out of range")
	expectRefused[uint](t, "-1", "negative")
	expectRefused[uint](t, "${-0.5}", "fractional")
}

func TestNumbersCoerceToFloats(t *testing.T) {
	expectCoerced(t, "1.5", 1.5)
	expectCoerced(t, "3", 3.0)
	expectCoerced(t, ".5", 0.5)
	expectCoerced(t, "${1/3}", 1.0/3)
	expectCoerced(t, "'2.5e-3'", 0.0025)
	expectCoerced(t, "1.5", float32(1.5))
	expectRefused[float32](t, "1e39", "out of range")
	expectRefused[float64](t, "yes", `cannot coerce string "yes" into float64`)
	expectRefused[float64](t, ".inf", "not a finite number")
	expectRefused[float64](t, "1e400", "out of the range")
}

func TestBoolsCoerceFromBoolsAndExactText(t *testing.T) {
	expectCoerced(t, "true", true)
	expectCoerced(t, "False", false)
	expectCoerced(t, "${1 > 0}", true)
	expectCoerced(t, "'false'", false)
	expectRefused[bool](t, "yes", `cannot coerce string "yes" into bool`)
	expectRefused[bool](t, "no", "only \"true\" and \"false\"")
	expectRefused[bool](t, "on", "only")
	expectRefused[bool](t, "'True'", "only")
	expectRefused[bool](t, "1", "cannot coerce number 1 into bool")
	expectRefused[bool](t, "[]", "cannot coerce list into bool")
}

func TestListsCoerceToSlicesAndArrays(t *testing.T) {
	expectCoerced(t, "[1, 2]", []int{1, 2})
	expectCoerced(t, "${list}", []string{"1", "2"})
	expectCoerced(t, "[]", []int{})
	expectCoerced(t, "[1, 2]", [2]int{1, 2})
	expectCoerced(t, "[[1], [2, 3]]", [][]int{{1}, {2, 3}})
	expectRefused[[3]int](t, "[1, 2]", "holds 2 items, the array 3")
	expectRefused[[]int](t, "[1, x]", "$.v[1]", `cannot coerce string "x" into int`)
	expectRefused[[]int](t, "'1,2'", "cannot coerce string")
	expectRefused[[]int](t, "{a: 1}", "cannot coerce object into []int")
}

func TestObjectsCoerceToMapsAndStructs(t *testing.T) {
	type pair struct {
		A int    `json:"a"`
		B string `json:"b,omitempty"`
	}
	expectCoerced(t, "{a: 1, b: x}", map[string]any{"a": int64(1), "b": "x"})
	expectCoerced(t, "{a: 1, b: 2}", map[string]int{"a": 1, "b": 2})
	expectCoerced(t, "\"${ {a: 1, b: 'x'} }\"", pair{A: 1, B: "x"})
	expectCoerced(t, "{a: '${ 1 + 1 }'}", pair{A: 2})
	expectRefused[pair](t, "{a: 1, c: 2}", "$.v.c", `has no field "c"`)
	expectRefused[pair](t, "{A: 1}", `has no field "A"`)
	expectRefused[pair](t, "[1]", "cannot coerce list into")
	expectRefused[map[string]int](t, "{a: x}", "$.v.a")
}

func TestNumbersIntoAnyAreInt64WhenWholeElseFloat64(t *testing.T) {
	expectCoerced[any](t, "3", int64(3))
	expectCoerced[any](t, "3.0", int64(3))
	expectCoerced[any](t, "3.5", 3.5)
	expectCoerced[any](t, "${2**64}", 18446744073709551616.0)
	expectCoerced[any](t, "18446744073709551615", 18446744073709551615.0)
	expectCoerced[any](t, "0x10", int64(16))
	expectCoerced[any](t, "'3'", "3")
}

func TestDurationsReadFromText(t *testing.T) {
	expectCoerced(t, "1m30s", 90*time.Second)
	expectCoerced(t, "${'2h'}", 2*time.Hour)
	expectRefused[time.Duration](t, "5", "durations are written as text")
	expectRefused[time.Duration](t, "soon", "invalid duration")
}

func TestTextUnmarshalersReadScalarsAsText(t *testing.T) {
	expectCoerced(t, "127.0.0.1", netip.MustParseAddr("127.0.0.1"))
	expectCoerced(t, "2026-10-18T10:00:00Z", time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC))
	expectRefused[netip.Addr](t, "nope", "cannot coerce string")
	expectRefused[netip.Addr](t, "[1]", "cannot coerce list")
}

type celsius struct {
	degrees float64
}

func (c *celsius) UnmarshalJSON(data []byte) error {
	var parsed struct {
		C float64 `json:"c"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	c.degrees = parsed.C
	return nil
}

func TestJSONUnmarshalersReadTheValueAsJSON(t *testing.T) {
	expectCoerced(t, "{c: '${ 20 + 1.5 }'}", celsius{degrees: 21.5})
	expectRefused[celsius](t, "warm", "cannot coerce string \"warm\" into jaml.celsius")
}

func TestPointersAreAllocated(t *testing.T) {
	got, err := coerce[*int](t, "${7}")
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, *got, 7)
	nested, err := coerce[**string](t, "x")
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, **nested, "x")
}

func TestCoercionErrorsNameThePathAndTypes(t *testing.T) {
	template := mustTemplate(t, "config:\n  servers:\n    - port: ${'eighty'}\n")
	type server struct {
		Port int `json:"port"`
	}
	type config struct {
		Servers []server `json:"servers"`
	}
	var out config
	err := template.Eval(&out, nil, "config")
	expectError(t, err, CoercionFailed, "$.config.servers[0].port (line 3, column 13)", `string "eighty"`, "into int")
}

func TestLongTextIsShortenedInErrors(t *testing.T) {
	_, err := coerce[int](t, strings.Repeat("x", 100))
	expectError(t, err, CoercionFailed, strings.Repeat("x", 40)+"…")
}
