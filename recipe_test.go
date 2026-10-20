package jaml

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestValueCodeHeldAsTextResolvesThroughADocument(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var held = "${p => /}/.test(p)}"
		written, err := yaml.Marshal(map[string]string{"v": held})
		if err != nil {
			t.Fatal(err)
		}
		var check = mustJaml(t, sb, string(written)).At("v")
		expectEqual(t, mustResolve[bool](t, t.Context(), check, "a}b"), true)
		expectEqual(t, mustResolve[bool](t, t.Context(), check, "ab"), false)
	})
}
