package jaml

import (
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentEvalsDoNotInterfere(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		template := mustTemplate(t, sb, `
name: ${{who}}
doubled: ${ n * 2 }
items: "${ Array.from({length: n % 5}, (_, i) => who + i) }"
leak: ${ globalThis.seen = (globalThis.seen || 0) + 1 }
`)
		type shape struct {
			Name    string   `json:"name"`
			Doubled int      `json:"doubled"`
			Items   []string `json:"items"`
			Leak    int      `json:"leak"`
		}
		var wait sync.WaitGroup
		failures := make(chan error, 64)
		for worker := range 16 {
			wait.Go(func() {
				for round := range 20 {
					n := worker*100 + round
					who := fmt.Sprintf("w%d", worker)
					var out shape
					if err := template.Eval(t.Context(), &out, map[string]any{"who": who, "n": n}); err != nil {
						failures <- err
						return
					}
					items := make([]string, n%5)
					for i := range items {
						items[i] = fmt.Sprintf("%s%d", who, i)
					}
					want := shape{Name: who, Doubled: n * 2, Items: items, Leak: 1}
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
		template := mustTemplate(t, sb, "v: ${ (shared.list.push(1), shared.list.length) }\n")
		vars := map[string]any{"shared": map[string]any{"list": []any{}}}
		var wait sync.WaitGroup
		for range 8 {
			wait.Go(func() {
				var out int
				if err := template.Eval(t.Context(), &out, vars, "v"); err != nil || out != 1 {
					t.Errorf("got %d, %v", out, err)
				}
			})
		}
		wait.Wait()
		expectEqual(t, vars["shared"], map[string]any{"list": []any{}})
	})
}
