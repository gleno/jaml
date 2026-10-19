package jaml

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

type KeyEmbedded struct {
	A      int
	Inner  int `json:"shared"`
	Folded int `json:"FOLD"`
}

type KeyOther struct {
	A int
	C int
}

type KeyPointed struct {
	P int
	A int `json:"a"`
}

type keyHidden struct {
	H int
}

type keyTricky struct {
	X1         int `json:"x"`
	X2         int `json:"X"`
	Name       int
	NAME       int `json:"name"`
	Skipped    int `json:"-"`
	Dash       int `json:"-,"`
	unexported int
	Fold       int
	Shared     int
	Weird      int `json:"we ird!"`
	Kelvin     int `json:"Kelvin"`
	KeyEmbedded
	KeyOther
	*KeyPointed
	keyHidden
}

type keyDeep struct {
	Outer int `json:"c"`
	KeyOther
}

func jsonPicks(target reflect.Type, key string) []int {
	var into = reflect.New(target)
	written := fmt.Sprintf(`{%q: 7}`, key)
	if err := json.Unmarshal([]byte(written), into.Interface()); err != nil {
		return nil
	}
	return setLeaf(into.Elem(), nil)
}

func setLeaf(value reflect.Value, index []int) []int {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return setLeaf(value.Elem(), index)
	case reflect.Struct:
		for i := range value.NumField() {
			if found := setLeaf(value.Field(i), append(slices.Clip(index), i)); found != nil {
				return found
			}
		}
	case reflect.Int:
		if value.Int() == 7 {
			return index
		}
	}
	return nil
}

func TestKeysResolveToTheFieldEncodingJSONPicks(t *testing.T) {
	keys := []string{
		"x", "X", "name", "Name", "NAME", "nAmE", "Skipped", "skipped", "-", "Dash", "dash", "unexported",
		"Fold", "fold", "FOLD", "Shared", "shared", "SHARED", "A", "a", "C", "c", "P", "p", "H", "h",
		"Inner", "inner", "we ird!", "WE IRD!", "Kelvin", "kelvin", "Kelvin", "missing", "",
	}
	picked := 0
	for _, target := range []reflect.Type{reflect.TypeFor[keyTricky](), reflect.TypeFor[keyDeep]()} {
		for _, key := range keys {
			want := jsonPicks(target, key)
			if want != nil {
				picked++
			}
			got, found := fieldFor(target, key)
			if !found {
				got = nil
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s key %q: jaml picks %v, encoding/json picks %v", target, key, got, want)
			}
		}
	}
	if picked < 30 {
		t.Fatalf("encoding/json picked a field for only %d keys; the table tests too little", picked)
	}
}

func TestAnExactKeyWinsOverAFoldedExpressionField(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		type target struct {
			Later *Expression `json:"x"`
			Now   any         `json:"X"`
		}
		got := mustResolve[target](t, t.Context(), mustJaml(t, sb, "x: ${ 1 }\nX: ${ 2 }\n"))
		expectEqual(t, got.Now, any(float64(2)))
		expectEqual(t, mustResolve[int](t, t.Context(), got.Later), 1)
	})
}
