package jaml

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

const backslash = "\\"

func escaped(code string) string {
	return backslash + code
}

var builtTexts = []string{
	`{}`, `[]`, `0`, `-0`, `[-0, 0, -0.0, 0e5, -0e-5]`, `true`, `false`, `null`, `""`,
	`{"b":1,"1":2,"a":3,"0":4,"b":5,"__proto__":6}`,
	`{"10":1,"9":2,"007":3,"-1":4,"4294967295":5,"4294967294":6,"1.5":7,"":8}`,
	`[12345678901234567890, 9007199254740993, 9007199254740992, -9007199254740993, 2147483648, -2147483649]`,
	`[1e21, 1e-7, 0.1, 5e-324, 1e-400, -1.5e-300, 1.7976931348623157e308, 1E2, 1e+2, 100, 1.0, 1.50]`,
	`{"__proto__":{"x":1},"constructor":2,"toString":3,"hasOwnProperty":4}`,
	" \t\n\r{ \"a\" : [ 1 , { } , [ ] ] }\n ",
	`["` + escaped("u00e9") + escaped("ud83d") + escaped("ude00") + escaped("n") + escaped("/") + escaped("\"") + escaped("\\") + escaped("t") + escaped("b") + escaped("f") + escaped("r") + `"]`,
	`{"` + escaped("u0030") + `":1,"0":2,"k` + escaped("u00e9") + `":3,"é":4,"😀":5}`,
	`["é😀", "plain", "` + escaped("u0000") + `"]`,
	strings.Repeat("[", maxDataDepth) + strings.Repeat("]", maxDataDepth),
	strings.Repeat(`{"a":`, maxDataDepth-1) + "{}" + strings.Repeat("}", maxDataDepth-1),
}

var refusedTexts = []string{`1e400`, `[-1e999]`, `{"a":1e309}`, `{`, `[1,]`, `{"a" 1}`, `tru`, `[1 2]`, `"open`, `{} {}`}

func TestAValueIsBuiltExactlyAsJSONParseBuildsIt(t *testing.T) {
	var texts = append(append([]string{}, builtTexts...), generatedTexts(500)...)
	eachRealm(t, func(t *testing.T, r _Realm, parse goja.Callable) {
		for _, text := range texts {
			parsed, err := parse(goja.Undefined(), r.vm.ToValue(text))
			if err != nil {
				t.Fatalf("JSON.parse refused %s: %v", clip(text, 80), err)
			}
			built, err := r.build(_Datum{encoded: []byte(text)}, _Mutable{})
			if err != nil {
				t.Fatalf("building %s: %v", clip(text, 80), err)
			}
			if difference := differ(parsed, built, "$"); difference != "" {
				t.Fatalf("building %s: %s", clip(text, 80), difference)
			}
		}
		for _, text := range refusedTexts {
			if _, err := parse(goja.Undefined(), r.vm.ToValue(text)); err == nil {
				t.Fatalf("JSON.parse accepted %s", text)
			}
			if built, err := r.build(_Datum{encoded: []byte(text)}, _Mutable{}); err == nil {
				t.Fatalf("building %s gave %v where JSON.parse refuses", text, built)
			}
		}
	})
}

func eachRealm(t *testing.T, body func(t *testing.T, r _Realm, parse goja.Callable)) {
	var vm = goja.New()
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	realm, err := captureRealm(_Pace{ctx: t.Context(), vm: vm}, _Runtime{builtins: mutableBuiltins})
	if err != nil {
		t.Fatal(err)
	}
	if exception := vm.Try(func() { body(t, realm, parse) }); exception != nil {
		t.Fatal(exception)
	}
}

func representation(value goja.Value) string {
	if _, isString := value.(goja.String); isString {
		return "string"
	}
	return fmt.Sprintf("%T", value)
}

func differ(want, got goja.Value, path string) string {
	if representation(want) != representation(got) {
		return fmt.Sprintf("%s is a %T, want a %T", path, got, want)
	}
	wanted, isObject := want.(*goja.Object)
	if !isObject {
		if !want.SameAs(got) {
			return fmt.Sprintf("%s is %v, want %v", path, got, want)
		}
		return ""
	}
	var object = got.(*goja.Object)
	if wanted.ClassName() != object.ClassName() || !wanted.Prototype().SameAs(object.Prototype()) {
		return fmt.Sprintf("%s is a %s, want a %s", path, object.ClassName(), wanted.ClassName())
	}
	var wantedKeys, keys = wanted.GetOwnPropertyNames(), object.GetOwnPropertyNames()
	if strings.Join(wantedKeys, "\x00") != strings.Join(keys, "\x00") {
		return fmt.Sprintf("%s has keys %q, want %q", path, keys, wantedKeys)
	}
	for _, key := range wantedKeys {
		if difference := differ(wanted.Get(key), object.Get(key), path+"["+key+"]"); difference != "" {
			return difference
		}
	}
	return ""
}

func generatedTexts(count int) []string {
	var random = rand.New(rand.NewPCG(7, 11))
	var texts = make([]string, count)
	for i := range texts {
		var text strings.Builder
		writeGenerated(&text, random, 0)
		texts[i] = text.String()
	}
	return texts
}

var generatedKeys = []string{"a", "b", "0", "1", "2", "10", "01", "-0", "1e3", "4294967295", "__proto__", "length", "é", "😀", "", " ", escaped("u0041"), "k" + escaped("n")}

var generatedNumbers = []string{"0", "-0", "1", "-1", "0.5", "1e21", "1e-7", "123456789012345678901", "9007199254740993", "2.5E+3", "-4.25e-2", "1.0", "100", "3.14159", "5e-324"}

var generatedStrings = []string{"", "x", "é", "😀", escaped("u00e9"), escaped("ud83d") + escaped("ude00"), escaped("t") + "tab", escaped("\"") + "q", "long " + strings.Repeat("y", 40)}

func writeGenerated(text *strings.Builder, random *rand.Rand, depth int) {
	var spaces = []string{"", " ", "\n", "\t "}
	text.WriteString(spaces[random.IntN(len(spaces))])
	var choice = random.IntN(10)
	switch {
	case depth < 5 && choice < 3:
		text.WriteByte('{')
		for index := range random.IntN(6) {
			if index > 0 {
				text.WriteByte(',')
			}
			text.WriteString(`"` + generatedKeys[random.IntN(len(generatedKeys))] + `":`)
			writeGenerated(text, random, depth+1)
		}
		text.WriteByte('}')
	case depth < 5 && choice < 5:
		text.WriteByte('[')
		for index := range random.IntN(6) {
			if index > 0 {
				text.WriteByte(',')
			}
			writeGenerated(text, random, depth+1)
		}
		text.WriteByte(']')
	case choice < 7:
		text.WriteString(generatedNumbers[random.IntN(len(generatedNumbers))])
	case choice < 9:
		text.WriteString(`"` + generatedStrings[random.IntN(len(generatedStrings))] + `"`)
	default:
		text.WriteString([]string{"true", "false", "null"}[random.IntN(3)])
	}
	text.WriteString(spaces[random.IntN(len(spaces))])
}

func BenchmarkBuildingAValue(b *testing.B) {
	var text = `[` + strings.TrimSuffix(strings.Repeat(`{"a":1,"name":"x","tags":["p","q"]},`, 100_000), ",") + `]`
	for _, protection := range []struct {
		name     string
		builtins _Builtins
	}{{"mutable", mutableBuiltins}, {"frozen", frozenBuiltins}} {
		var vm = goja.New()
		realm, err := captureRealm(_Pace{ctx: b.Context(), vm: vm}, _Runtime{builtins: protection.builtins})
		if err != nil {
			b.Fatal(err)
		}
		parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
		b.Run(protection.name+"/build", func(b *testing.B) {
			for b.Loop() {
				if _, err := realm.build(_Datum{encoded: []byte(text)}, realm.protection); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(protection.name+"/parse", func(b *testing.B) {
			for b.Loop() {
				if _, err := parse(goja.Undefined(), vm.ToValue(text)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
