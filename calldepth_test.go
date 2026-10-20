package jaml

import "testing"

const recursion = "v: \"${ (function f(n) { return n === 0 ? 0 : f(n - 1) + 1 })(depth) }\"\n"

func TestTheCallDepthLimitDefaultsTo4096(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out int
		var err = resolveInto(withValues(t, map[string]any{"depth": 5000}), &out, mustJaml(t, sb, recursion).At("v"))
		expectError(t, err, EvaluationFailed, "$.v: ", "exceeded the maximum call depth of 4096")
	})
}

func TestACallDepthLimitLetsRecursionGoDeeper(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, recursion)
		expectEqual(t, evalAs[int](t, document, map[string]any{"depth": 5000}, "v"), 5000)
		var through = mustJaml(t, sb, "v: \"${ (function f(n) { return n === 0 ? 0 : [n].map((m) => f(m - 1))[0] + 1 })(10000) }\"\n")
		expectEqual(t, evalAs[int](t, through, nil, "v"), 10000)
		var out int
		expectError(t, resolveInto(withValues(t, map[string]any{"depth": 40000}), &out, document.At("v")), EvaluationFailed, "exceeded the maximum call depth of 32768")
	}, WithCallDepthLimit(32768))
}

func TestACallDepthLimitCanBeShallower(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, recursion)
		expectEqual(t, evalAs[int](t, document, map[string]any{"depth": 50}, "v"), 50)
		var out int
		expectError(t, resolveInto(withValues(t, map[string]any{"depth": 200}), &out, document.At("v")), EvaluationFailed, "exceeded the maximum call depth of 100")
	}, WithCallDepthLimit(100))
}

func TestACallDepthLimitOutOfRangeIsRefused(t *testing.T) {
	for _, kind := range allSandboxKinds {
		for _, depth := range []int{0, -1, maxCallDepthLimit + 1} {
			if kind.isolated && isolationSupported() != nil {
				continue
			}
			_, err := kind.make(t.Context(), WithCallDepthLimit(depth))
			expectError(t, err, InvalidExpression, "call depth limit must be between 1 and 32768")
		}
	}
}

func TestACallDepthLimitOfOneStillRunsACall(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		expectEqual(t, mustResolve[int](t, t.Context(), mustExpression(t, sb, "1")), 1)
	}, WithCallDepthLimit(1))
}

func TestACallDepthLimitOfThreeRefusesRecursion(t *testing.T) {
	allSandboxKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var out any
		var err = resolveInto(t.Context(), &out, mustExpression(t, sb, "(f => f(f))(f => f(f))"))
		expectError(t, err, EvaluationFailed, "$: ", "exceeded the maximum call depth of 3")
	}, WithCallDepthLimit(3))
}

func TestProtectionCostsNoCallDepth(t *testing.T) {
	var endings = map[string]string{"push": "[].push(1)", "sort": "[2, 1].sort((a, b) => a - b)", "read": "({}).toString"}
	for name, ending := range endings {
		t.Run(name, func(t *testing.T) {
			var reached = map[string]int{}
			for _, kind := range allSandboxKinds {
				var sb = mustSandbox(t, kind, WithCallDepthLimit(50))
				reached[kind.name] = deepestCall(t, mustExpression(t, sb, "(function f(k) { return k === 0 ? ("+ending+") : f(k - 1) })(depth)"))
			}
			expectEqual(t, reached, map[string]int{trustedKind.name: reached[trustedKind.name], isolatedKind.name: reached[trustedKind.name], unprotectedKind.name: reached[trustedKind.name]})
		})
	}
}

func deepestCall(t *testing.T, expression *Expression) int {
	t.Helper()
	for depth := range 60 {
		var out any
		if err := resolveInto(withValues(t, map[string]any{"depth": depth}), &out, expression); err != nil {
			expectError(t, err, EvaluationFailed, "exceeded the maximum call depth of 50")
			return depth - 1
		}
	}
	t.Fatal("no call reached the limit of 50")
	return 0
}
