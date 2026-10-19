package jaml

import (
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentEvalsDoNotInterfere(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
name: ${{who}}
doubled: ${ n * 2 }
items: "${ Array.from({length: n % 5}, (_, i) => who + i) }"
leak: ${ globalThis.seen = (globalThis.seen || 0) + 1 }
`)
		type _Decoded struct {
			Name    string   `json:"name"`
			Doubled int      `json:"doubled"`
			Items   []string `json:"items"`
			Leak    int      `json:"leak"`
		}
		var wait sync.WaitGroup
		var failures = make(chan error, 64)
		for worker := range 16 {
			wait.Go(func() {
				for round := range 20 {
					var n = worker*100 + round
					var who = fmt.Sprintf("w%d", worker)
					var out _Decoded
					if err := resolveInto(valued(t.Context(), map[string]any{"who": who, "n": n}), &out, document); err != nil {
						failures <- err
						return
					}
					var items = make([]string, n%5)
					for i := range items {
						items[i] = fmt.Sprintf("%s%d", who, i)
					}
					var want = _Decoded{Name: who, Doubled: n * 2, Items: items, Leak: 1}
					if fmt.Sprint(out) != fmt.Sprint(want) {
						failures <- fmt.Errorf("got %+v, want %+v", out, want)
						return
					}
				}
			})
		}
		wait.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
	})
}

func TestSharedVarsAreNotMutated(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, "v: ${ [...shared.list, 1].length }\n")
		var vars = map[string]any{"shared": map[string]any{"list": []any{}}}
		var wait sync.WaitGroup
		for range 8 {
			wait.Go(func() {
				var out int
				if err := resolveInto(valued(t.Context(), vars), &out, document.At("v")); err != nil || out != 1 {
					t.Errorf("got %d, %v", out, err)
				}
			})
		}
		wait.Wait()
		expectEqual(t, vars["shared"], map[string]any{"list": []any{}})
	})
}
