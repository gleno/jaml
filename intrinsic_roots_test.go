package jaml

import (
	"fmt"
	"testing"
)

func TestReplacingGlobalConstructorsKeepsOriginalIntrinsicsProtected(t *testing.T) {
	var families = map[string]string{
		"Array":          `Object.getPrototypeOf([])`,
		"String":         `Object.getPrototypeOf("text")`,
		"Number":         `Object.getPrototypeOf(1)`,
		"BigInt":         `Object.getPrototypeOf(1n)`,
		"Symbol":         `Object.getPrototypeOf(Object.getOwnPropertySymbols(Array.prototype)[0])`,
		"Boolean":        `Object.getPrototypeOf(true)`,
		"RegExp":         `Object.getPrototypeOf(/x/)`,
		"TypeError":      `(() => {try {null.x} catch (e) {return Object.getPrototypeOf(e)}})()`,
		"RangeError":     `(() => {try {[].length = -1} catch (e) {return Object.getPrototypeOf(e)}})()`,
		"ReferenceError": `(() => {try {missingName} catch (e) {return Object.getPrototypeOf(e)}})()`,
		"SyntaxError":    `(() => {try {JSON.parse("{")} catch (e) {return Object.getPrototypeOf(e)}})()`,
		"URIError":       `(() => {try {decodeURIComponent("%")} catch (e) {return Object.getPrototypeOf(e)}})()`,
	}
	eachProtection(t, func(t *testing.T, sb *Sandbox, protected bool) {
		for name, original := range families {
			t.Run(name, func(t *testing.T) {
				var setup = fmt.Sprintf(`
const descriptor = Object.getOwnPropertyDescriptor(%[1]s, "prototype");
if (descriptor.writable || descriptor.configurable) throw new Error("the native prototype link changed");
%[1]s.prototype.setup = {detail: {value: "original"}};
%[1]s.prototype.constructor = null;
%[1]s = function Replacement() {};
%[1]s.extension = {detail: {value: "replacement"}};
`, name)
				var source = fmt.Sprintf(`(() => {
const original = %s;
let written = "written";
try {original.polluted = true} catch (e) {written = e.name}
return [written, Object.isFrozen(original), Object.isFrozen(original.setup.detail), Object.isFrozen(%s), Object.isFrozen(%s.extension.detail), original.setup.detail.value, %s.extension.detail.value];
})()`, original, name, name, name)
				var written = "written"
				if protected {
					written = "TypeError"
				}
				var got = mustResolve[[]any](t, WithScript(t.Context(), setup), mustExpression(t, sb, source))
				expectEqual(t, got, []any{written, protected, protected, protected, protected, "original", "replacement"})
			})
		}
	})
}

func TestReplacedArrayConstructorCannotPolluteValuesOrLaterLeaves(t *testing.T) {
	eachProtection(t, func(t *testing.T, sb *Sandbox, protected bool) {
		var setup = `Array.prototype.constructor = null; Array = function Replacement() {};`
		var document = mustJaml(t, sb, `
write: ${ (() => {try {Object.getPrototypeOf([]).polluted = true; return "written"} catch (e) {return e.name}})() }
literal: ${ [].polluted === true }
value: ${ items.polluted === true }
same: ${ Object.getPrototypeOf(items) === Object.getPrototypeOf([]) }
`)
		var ctx = WithValues(WithScript(t.Context(), setup), map[string]any{"items": []int{1, 2}})
		var written = "written"
		if protected {
			written = "TypeError"
		}
		expectEqual(t, mustResolve[map[string]any](t, ctx, document), map[string]any{"write": written, "literal": !protected, "value": !protected, "same": true})
	})
}

func TestAnUnlinkedGeneratorPrototypeRemainsProtected(t *testing.T) {
	eachProtection(t, func(t *testing.T, sb *Sandbox, protected bool) {
		var setup = `delete Object.getPrototypeOf(function* () {}).prototype;`
		var source = `(() => {
const original = Object.getPrototypeOf(Object.getPrototypeOf((function* () {})()));
try {original.polluted = true} catch (e) {return [e.name, Object.isFrozen(original)]}
return ["written", Object.isFrozen(original)];
})()`
		var written = "written"
		if protected {
			written = "TypeError"
		}
		expectEqual(t, mustResolve[[]any](t, WithScript(t.Context(), setup), mustExpression(t, sb, source)), []any{written, protected})
	})
}
