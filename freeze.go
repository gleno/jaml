package jaml

import (
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

const intrinsicsFreezer = `(() => {
	"use strict";
	const { freeze, isFrozen, getOwnPropertyDescriptor, getPrototypeOf, defineProperty, hasOwn } = Object;
	const { ownKeys, apply } = Reflect;
	const settles = Reflect.defineProperty;
	const bound = Function.prototype.call.bind(Function.prototype.bind);
	const Seen = Set;
	const has = bound(Function.prototype.call, Set.prototype.has);
	const add = bound(Function.prototype.call, Set.prototype.add);
	const global = globalThis;
	const Refusal = TypeError;
	const typedArray = getPrototypeOf(Uint8Array).prototype;
	const overridable = [
		[Error.prototype, ["name", "message"]],
		[EvalError.prototype, ["name", "message"]],
		[RangeError.prototype, ["name", "message"]],
		[ReferenceError.prototype, ["name", "message"]],
		[SyntaxError.prototype, ["name", "message"]],
		[TypeError.prototype, ["name", "message"]],
		[URIError.prototype, ["name", "message"]],
		[Object.prototype, ["toString", "valueOf", "constructor", "hasOwnProperty", "toLocaleString", "isPrototypeOf", "propertyIsEnumerable"]],
		[Function.prototype, ["toString", "name", "constructor"]],
		[Array.prototype, ["constructor", "toString", "push"]],
		[typedArray, ["constructor", "toString", "push"]],
		[typeof Promise === "function" ? Promise.prototype : null, ["constructor"]],
		[typeof Symbol === "function" ? Symbol.prototype : null, ["constructor"]],
	];
	const guardedMethods = [
		[Array.prototype, ["sort", "reverse", "fill", "copyWithin", "splice", "push", "pop", "shift", "unshift"], (self) => self],
		[Object, ["defineProperty", "defineProperties", "assign", "setPrototypeOf"], (self, args) => args[0]],
	];
	const isReadOnly = (target) => target !== null && (typeof target === "object" || typeof target === "function") && isFrozen(target);
	const guardedAt = (home, key, targetOf, refuse) => {
		const original = home[key];
		if (typeof original !== "function") {
			return;
		}
		const guarded = {
			[key](...args) {
				let fromCallback = false;
				const callback = args[0];
				if (key === "sort" && typeof callback === "function") {
					args[0] = (a, b) => {
						try {
							return callback(a, b);
						} catch (failure) {
							fromCallback = true;
							throw failure;
						}
					};
				}
				try {
					return apply(original, this, args);
				} catch (failure) {
					if (!fromCallback && failure instanceof Refusal && isReadOnly(targetOf(this, args))) {
						refuse();
					}
					throw failure;
				}
			},
		}[key];
		defineProperty(guarded, "length", { __proto__: null, value: original.length });
		defineProperty(home, key, { __proto__: null, value: guarded });
	};
	const overridableAt = (home, key, refuse) => {
		const described = getOwnPropertyDescriptor(home, key);
		if (described === undefined || !hasOwn(described, "value") || !described.configurable) {
			return null;
		}
		const value = described.value;
		defineProperty(home, key, {
			__proto__: null,
			get() {
				return value;
			},
			set(next) {
				if (this === home || this === null || (typeof this !== "object" && typeof this !== "function")) {
					refuse();
				}
				const own = getOwnPropertyDescriptor(this, key);
				const replacement = own === undefined ? { __proto__: null, value: next, writable: true, enumerable: true, configurable: true } : { __proto__: null, value: next };
				if ((own !== undefined && !(hasOwn(own, "writable") && own.writable === true)) || !settles(this, key, replacement)) {
					refuse();
				}
			},
			enumerable: described.enumerable,
			configurable: true,
		});
		return value;
	};
	const shadowed = (object, key) => {
		throw new Refusal("a script shadowed a property descriptor field on Object.prototype, so " + String(key) + " cannot be frozen");
	};
	const isAccessor = (described) => hasOwn(described, "get") && hasOwn(described, "set");
	const pristine = ownKeys(global);
	const intrinsics = [
		getPrototypeOf(function* () {}),
		getPrototypeOf(async function () {}),
		getPrototypeOf([][Symbol.iterator]()),
		getPrototypeOf(new Map()[Symbol.iterator]()),
		getPrototypeOf(new Set()[Symbol.iterator]()),
		getPrototypeOf(""[Symbol.iterator]()),
		getPrototypeOf("a".matchAll(/a/g)),
		getPrototypeOf(Uint8Array),
	];
	const refuserOf = (refusal) => () => {
		throw new Refusal(refusal);
	};
	const guard = (refuse) => {
		for (let i = 0; i < guardedMethods.length; i++) {
			const keys = guardedMethods[i][1];
			for (let j = 0; j < keys.length; j++) {
				guardedAt(guardedMethods[i][0], keys[j], guardedMethods[i][2], refuse);
			}
		}
	};
	const tame = (refusal) => {
		const refuse = refuserOf(refusal);
		guard(refuse);
		const charted = [];
		let count = 0;
		for (let i = 0; i < pristine.length; i++) {
			const described = getOwnPropertyDescriptor(global, pristine[i]);
			if (described === undefined) {
				continue;
			}
			if (hasOwn(described, "value")) {
				charted[count++] = described.value;
				defineProperty(global, pristine[i], { __proto__: null, writable: false, configurable: false });
				continue;
			}
			if (!isAccessor(described)) {
				shadowed(global, pristine[i]);
			}
			defineProperty(global, pristine[i], { __proto__: null, configurable: false });
		}
		for (let i = 0; i < intrinsics.length; i++) {
			charted[count++] = intrinsics[i];
		}
		for (let i = 0; i < overridable.length; i++) {
			const home = overridable[i][0];
			const keys = overridable[i][1];
			for (let j = 0; home !== null && j < keys.length; j++) {
				charted[count++] = overridableAt(home, keys[j], refuse);
			}
		}
		const loose = [];
		const names = ownKeys(global);
		for (let i = 0, held = 0; i < names.length; i++) {
			const described = getOwnPropertyDescriptor(global, names[i]);
			if (!hasOwn(described, "value")) {
				if (!isAccessor(described)) {
					shadowed(global, names[i]);
				}
				loose[held++] = described.get;
				loose[held++] = described.set;
			}
		}
		return { charted, loose };
	};
	const walk = (roots, reached, seen) => {
		const pending = [];
		let waiting = 0;
		for (let i = roots.length - 1; i >= 0; i--) {
			pending[waiting++] = roots[i];
			pending[waiting++] = null;
			pending[waiting++] = 0;
			pending[waiting++] = i;
		}
		while (waiting > 0) {
			const key = pending[--waiting];
			const kind = pending[--waiting];
			const parent = pending[--waiting];
			const object = pending[--waiting];
			if (object === null || object === global || (typeof object !== "object" && typeof object !== "function") || has(seen, object)) {
				continue;
			}
			add(seen, object);
			reached(object, parent, kind, key);
			const keys = ownKeys(object);
			for (let i = 0; i < keys.length; i++) {
				const described = getOwnPropertyDescriptor(object, keys[i]);
				if (hasOwn(described, "value")) {
					pending[waiting++] = described.value;
					pending[waiting++] = object;
					pending[waiting++] = 1;
					pending[waiting++] = keys[i];
					continue;
				}
				if (!isAccessor(described)) {
					shadowed(object, keys[i]);
				}
				pending[waiting++] = described.set;
				pending[waiting++] = object;
				pending[waiting++] = 3;
				pending[waiting++] = keys[i];
				pending[waiting++] = described.get;
				pending[waiting++] = object;
				pending[waiting++] = 2;
				pending[waiting++] = keys[i];
			}
			pending[waiting++] = getPrototypeOf(object);
			pending[waiting++] = object;
			pending[waiting++] = 4;
			pending[waiting++] = null;
		}
	};
	const freezeAll = (refusal, chart) => {
		const { charted, loose } = tame(refusal);
		const seen = new Seen();
		const reached = [];
		let count = 0;
		const collect = (object) => {
			reached[count++] = object;
		};
		if (chart === undefined) {
			walk(charted, collect, seen);
		} else {
			const frozen = chart(charted);
			for (let i = 0; loose.length > 0 && i < frozen.length; i++) {
				add(seen, frozen[i]);
			}
		}
		walk(loose, collect, seen);
		for (let i = 0; i < count; i++) {
			freeze(reached[i]);
		}
	};
	const chartOf = (refusal) => {
		const ids = new Map();
		let routes = "";
		const routeTo = (parent, kind, key) => {
			if (kind === 0) {
				return "r[" + key + "]";
			}
			const from = "o[" + ids.get(parent) + "]";
			if (kind === 4) {
				return "p(" + from + ")";
			}
			const named = typeof key === "symbol" ? "k(" + from + "," + ownKeys(parent).indexOf(key) + ")" : JSON.stringify(key);
			if (kind === 1) {
				return from + "[" + named + "]";
			}
			return "g(" + from + "," + named + ")." + (kind === 2 ? "get" : "set");
		};
		walk(tame(refusal).charted, (object, parent, kind, key) => {
			routes += "o[" + ids.size + "]=f(" + routeTo(parent, kind, key) + ");";
			ids.set(object, ids.size);
		}, new Seen());
		return "(r)=>{const g=Object.getOwnPropertyDescriptor,p=Object.getPrototypeOf,f=Object.freeze,k=(o,n)=>Reflect.ownKeys(o)[n],o=[];" + routes + "return o;}";
	};
	return { freeze: freezeAll, chart: chartOf, guard: (refusal) => guard(refuserOf(refusal)) };
})()`

var freezerProgram = sync.OnceValue(func() *goja.Program {
	return goja.MustCompile(freezerFile, intrinsicsFreezer, true)
})

var pristineChart = sync.OnceValues(func() (*goja.Program, error) {
	var realm = captureRealm(goja.New())
	made, err := realm.vm.RunProgram(freezerProgram())
	if err != nil {
		return nil, err
	}
	chart, _ := goja.AssertFunction(made.ToObject(realm.vm).Get("chart"))
	source, err := chart(goja.Undefined(), realm.vm.ToValue(readOnlyRefusal))
	if err != nil {
		return nil, err
	}
	return goja.Compile(chartFile, source.String(), true)
})

func chartTheIntrinsics() {
	_, _ = pristineChart()
}

type _Freezer struct {
	freeze goja.Callable
	chart  goja.Value
}

type _MethodGuard struct {
	guard goja.Callable
}

func (r _Realm) builtinsSettler(plan _SessionPlan) (func(_Realm) error, error) {
	made, err := r.vm.RunProgram(freezerProgram())
	if err != nil {
		return nil, fmt.Errorf("preparing to settle the built-ins: %w", r.explain(err))
	}
	var exports = made.ToObject(r.vm)
	if plan.builtins == mutableBuiltins {
		guard, _ := goja.AssertFunction(exports.Get("guard"))
		return _MethodGuard{guard: guard}.apply, nil
	}
	freeze, _ := goja.AssertFunction(exports.Get("freeze"))
	if len(plan.scripts) > 0 {
		return _Freezer{freeze: freeze, chart: goja.Undefined()}.apply, nil
	}
	chart, err := pristineChart()
	if err != nil {
		return nil, fmt.Errorf("charting the built-ins: %w", err)
	}
	charted, err := r.vm.RunProgram(chart)
	if err != nil {
		return nil, fmt.Errorf("preparing to freeze the built-ins: %w", r.explain(err))
	}
	return _Freezer{freeze: freeze, chart: charted}.apply, nil
}

func (f _Freezer) apply(r _Realm) error {
	if _, err := f.freeze(goja.Undefined(), r.vm.ToValue(readOnlyRefusal), f.chart); err != nil {
		return fmt.Errorf("freezing the built-ins: %w", r.explain(err))
	}
	return nil
}

func (g _MethodGuard) apply(r _Realm) error {
	if _, err := g.guard(goja.Undefined(), r.vm.ToValue(readOnlyRefusal)); err != nil {
		return fmt.Errorf("guarding the built-in methods: %w", r.explain(err))
	}
	return nil
}

type _Builtins int

const (
	frozenBuiltins _Builtins = iota
	mutableBuiltins
)

var builtinsNames = map[_Builtins]string{frozenBuiltins: "frozen", mutableBuiltins: "mutable"}

func (b _Builtins) String() string {
	return builtinsNames[b]
}

func builtinsNamed(name string) (_Builtins, bool) {
	for builtins, known := range builtinsNames {
		if known == name {
			return builtins, true
		}
	}
	return frozenBuiltins, false
}

func (s *_Session) builtinsSettler(plan _SessionPlan) (settle func(_Realm) error, err error) {
	err = s.enter(func(r _Realm) (err error) { settle, err = r.builtinsSettler(plan); return err })
	return settle, err
}
