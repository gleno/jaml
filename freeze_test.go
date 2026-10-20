package jaml

import (
	"testing"

	"github.com/dop251/goja"
)

const censusMaker = `(() => {
	const { getOwnPropertyDescriptor, getPrototypeOf, isExtensible, hasOwn } = Object;
	const { ownKeys, apply } = Reflect;
	const bound = Function.prototype.call.bind(Function.prototype.bind);
	const has = bound(Function.prototype.call, Set.prototype.has);
	const add = bound(Function.prototype.call, Set.prototype.add);
	const sort = bound(Function.prototype.call, Array.prototype.sort);
	const includes = bound(Function.prototype.call, String.prototype.includes);
	const goNamed = (object) => {
		const described = typeof object === "function" ? getOwnPropertyDescriptor(object, "name") : undefined;
		const name = described !== undefined && hasOwn(described, "value") ? described.value : "";
		return typeof name === "string" && (includes(name, "github.com") || includes(name, "/") || includes(name, ".func"));
	};
	const Seen = Set;
	const named = String;
	const self = globalThis;
	const strictArguments = (function () { "use strict"; return arguments })();
	const intrinsics = [
		[getOwnPropertyDescriptor(strictArguments, "callee").get, "arguments.callee<get>"],
		[getOwnPropertyDescriptor(strictArguments, "callee").set, "arguments.callee<set>"],
		[getPrototypeOf(function* () {}), "%GeneratorFunction.prototype%"],
		[getPrototypeOf(getPrototypeOf((function* () {})())), "%GeneratorPrototype%"],
		[getPrototypeOf(async function () {}), "%AsyncFunction.prototype%"],
		[getPrototypeOf([][Symbol.iterator]()), "%ArrayIteratorPrototype%"],
		[getPrototypeOf(new Map()[Symbol.iterator]()), "%MapIteratorPrototype%"],
		[getPrototypeOf(new Set()[Symbol.iterator]()), "%SetIteratorPrototype%"],
		[getPrototypeOf(""[Symbol.iterator]()), "%StringIteratorPrototype%"],
		[getPrototypeOf("a".matchAll(/a/g)), "%RegExpStringIteratorPrototype%"],
		[getPrototypeOf(Uint8Array), "%TypedArray%"],
	];
	const census = () => {
		const loose = [];
		let found = 0;
		const seen = new Seen();
		const pending = [];
		let waiting = 0;
		const reach = (value, path) => {
			if (value !== null && (typeof value === "object" || typeof value === "function")) {
				pending[waiting++] = value;
				pending[waiting++] = path;
			}
		};
		for (let i = 0; i < intrinsics.length; i++) {
			reach(intrinsics[i][0], intrinsics[i][1]);
		}
		const names = ownKeys(self);
		for (let i = 0; i < names.length; i++) {
			const described = getOwnPropertyDescriptor(self, names[i]);
			const path = "globalThis." + named(names[i]);
			if (described.configurable || (hasOwn(described, "value") && described.writable)) {
				loose[found++] = path;
			}
			reach(hasOwn(described, "value") ? described.value : null, path);
			reach(described.get, path + "<get>");
			reach(described.set, path + "<set>");
		}
		while (waiting > 0) {
			const path = pending[--waiting];
			const object = pending[--waiting];
			if (object === self || object === census || has(seen, object)) {
				continue;
			}
			add(seen, object);
			if (isExtensible(object)) {
				loose[found++] = path;
			}
			if (goNamed(object)) {
				loose[found++] = path + "<go name>";
			}
			const keys = ownKeys(object);
			for (let i = 0; i < keys.length; i++) {
				const described = getOwnPropertyDescriptor(object, keys[i]);
				const at = path + "." + named(keys[i]);
				if (described.configurable || (hasOwn(described, "value") && described.writable)) {
					loose[found++] = at;
				}
				if (hasOwn(described, "value")) {
					reach(described.value, at);
					continue;
				}
				if (!hasOwn(described, "get") || !hasOwn(described, "set")) {
					loose[found++] = at + "<shadowed>";
				}
				reach(described.get, at + "<get>");
				reach(described.set, at + "<set>");
				try {
					reach(apply(described.get, object, []), at + "<got>");
				} catch {}
			}
			reach(getPrototypeOf(object), path + ".__proto__");
		}
		return sort(loose);
	};
	return census;
})()`

const intrinsicsCensus = "(" + censusMaker + ")()"

const censusBeforeTampering = `Object.defineProperty(globalThis, "census", { value: ` + censusMaker + ` })`

const builtInExtensions = `
Array.prototype.total = function () { return this.reduce((sum, n) => sum + n, 0) };
Array.prototype.total.meta = { tags: [] };
Math.settings = { precision: 2 };
Object.freeze(Math);
Object.defineProperty(globalThis, "probe", { get: function () { return 1 }, set: function (next) {}, configurable: false });
`

const builtInTampering = `
Set.prototype.has = function () { return true };
Map.prototype.has = function () { return true };
Array.prototype.push = function () { return 0 };
Array.prototype[Symbol.iterator] = function* () {};
`

func TestEveryBuiltInIsFrozenAfterTheFreeze(t *testing.T) {
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var census = mustExpression(t, sb, intrinsicsCensus)
		expectEqual(t, mustResolve[[]string](t, t.Context(), census), []string{})
		expectEqual(t, mustResolve[[]string](t, withValues(t, map[string]any{"user": map[string]any{"tags": []string{"a"}}}), census), []string{})
		expectEqual(t, mustResolve[[]string](t, WithScript(t.Context(), builtInExtensions), census), []string{})
	})
}

func TestAScriptTamperingWithBuiltInsCannotDefeatTheFreeze(t *testing.T) {
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var ctx = WithScript(WithScript(WithScript(t.Context(), censusBeforeTampering), builtInExtensions), builtInTampering)
		expectEqual(t, mustResolve[[]string](t, ctx, mustExpression(t, sb, "census()")), []string{})
	})
}

func TestAScriptShadowingDescriptorFieldsCannotDefeatTheFreeze(t *testing.T) {
	var reached = "[Object.getPrototypeOf(Object.getPrototypeOf((function* () {})())), Math.settings, Array.prototype.total.meta.tags, Function.prototype.call, Proxy.revocable, Object.prototype.toString, [].push].every(Object.isFrozen)"
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var assigned = WithScript(WithScript(t.Context(), builtInExtensions), "Object.prototype.value = { planted: [] }; Object.prototype.get = function () {}; Object.prototype.writable = true")
		expectEqual(t, mustResolve[bool](t, assigned, mustExpression(t, sb, reached)), true)
		var defined = WithScript(t.Context(), "Object.defineProperty(Object.prototype, 'value', { value: 1 })")
		var _, err = Resolve[bool](defined, mustExpression(t, sb, reached))
		expectError(t, err, EvaluationFailed, "freezing the built-ins", "a script shadowed a property descriptor field on Object.prototype")
	})
}

func TestTheChartedFreezeReachesAccessorsOnTheGlobalObject(t *testing.T) {
	var realm = captureRealm(goja.New(), defaultCallDepthLimit)
	settle, err := realm.builtinsSettler(_SessionPlan{})
	if err != nil {
		t.Fatal(err)
	}
	var getter = _Native{name: "get bound", call: func(goja.FunctionCall) goja.Value { return goja.Undefined() }}.on(realm.vm)
	var setter = _Native{name: "set bound", length: 1, call: func(goja.FunctionCall) goja.Value { return goja.Undefined() }}.on(realm.vm)
	if err := realm.vm.GlobalObject().DefineAccessorProperty("bound", getter, setter, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
		t.Fatal(err)
	}
	if err := settle(realm); err != nil {
		t.Fatal(err)
	}
	loose, err := realm.vm.RunString(intrinsicsCensus)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, loose.Export(), []any{})
}

func TestThePristineIntrinsicsAreCharted(t *testing.T) {
	if _, err := pristineChart(); err != nil {
		t.Fatal(err)
	}
}

func TestALeafCannotHijackFunctionPrototypeForALaterLeaf(t *testing.T) {
	protectedKinds.each(t, func(t *testing.T, sb *Sandbox) {
		var document = mustJaml(t, sb, `
a: "${ [() => Object.defineProperty(Function.prototype.call, 'call', { value: () => 'hijacked' }), () => Object.setPrototypeOf(Function.prototype.apply, null), () => { Proxy.revocable.leak = 42 }, () => { Function.prototype.bind.x = 1 }, () => { Object.prototype.toString.x = 1 }, () => { [].push.x = 1 }, () => { Math = {} }].map((write) => { try { write(); return 'written' } catch (e) { return e.name } }) }"
b: "${ [Math.max.call.call(Math.max, null, 1, 2), typeof Function.prototype.apply.call, Proxy.revocable.leak === undefined, Function.prototype.bind.x === undefined, Object.prototype.toString.x === undefined, [].push.x === undefined, typeof Math.max] }"
`)
		expectEqual(t, mustResolve[map[string]any](t, t.Context(), document), map[string]any{
			"a": []any{"TypeError", "TypeError", "TypeError", "TypeError", "TypeError", "TypeError", "TypeError"},
			"b": []any{float64(2), "function", true, true, true, true, "function"},
		})
	})
}
