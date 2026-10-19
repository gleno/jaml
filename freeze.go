package jaml

import (
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

const intrinsicsFreezer = `(() => {
	"use strict";
	const { freeze, isFrozen, getOwnPropertyDescriptor, getPrototypeOf, defineProperty } = Object;
	const { ownKeys, apply } = Reflect;
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
	const guardedAt = (home, key, targetOf, refusal) => {
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
						throw new Refusal(refusal);
					}
					throw failure;
				}
			},
		}[key];
		defineProperty(guarded, "length", { value: original.length });
		defineProperty(home, key, { value: guarded });
	};
	const overridableAt = (home, key) => {
		const described = getOwnPropertyDescriptor(home, key);
		if (described === undefined || !("value" in described) || !described.configurable) {
			return;
		}
		const value = described.value;
		defineProperty(home, key, {
			get() {
				return value;
			},
			set(next) {
				if (this === home) {
					throw new Refusal("Cannot assign to read only property '" + key + "' of a frozen built-in");
				}
				if (getOwnPropertyDescriptor(this, key) !== undefined) {
					this[key] = next;
					return;
				}
				defineProperty(this, key, { value: next, writable: true, enumerable: true, configurable: true });
			},
			enumerable: described.enumerable,
			configurable: true,
		});
	};
	const roots = [];
	let count = 0;
	const names = ownKeys(global);
	for (let i = 0; i < names.length; i++) {
		const described = getOwnPropertyDescriptor(global, names[i]);
		if ("value" in described) {
			roots[count++] = described.value;
		}
	}
	roots[count++] = getPrototypeOf(function* () {});
	roots[count++] = getPrototypeOf(async function () {});
	roots[count++] = getPrototypeOf([][Symbol.iterator]());
	roots[count++] = getPrototypeOf(new Map()[Symbol.iterator]());
	roots[count++] = getPrototypeOf(new Set()[Symbol.iterator]());
	roots[count++] = getPrototypeOf(""[Symbol.iterator]());
	roots[count++] = getPrototypeOf("a".matchAll(/a/g));
	roots[count++] = getPrototypeOf(Uint8Array);
	return (refusal) => {
		for (let i = 0; i < guardedMethods.length; i++) {
			const keys = guardedMethods[i][1];
			for (let j = 0; j < keys.length; j++) {
				guardedAt(guardedMethods[i][0], keys[j], guardedMethods[i][2], refusal);
			}
		}
		for (let i = 0; i < overridable.length; i++) {
			const home = overridable[i][0];
			const keys = overridable[i][1];
			for (let j = 0; home !== null && j < keys.length; j++) {
				overridableAt(home, keys[j]);
			}
		}
		const pending = [];
		let waiting = 0;
		for (let i = 0; i < count; i++) {
			pending[waiting++] = roots[i];
		}
		while (waiting > 0) {
			const object = pending[--waiting];
			if (object === null || object === global || (typeof object !== "object" && typeof object !== "function") || isFrozen(object)) {
				continue;
			}
			freeze(object);
			if (typeof object === "function" && !("prototype" in object)) {
				continue;
			}
			const keys = ownKeys(object);
			for (let i = 0; i < keys.length; i++) {
				const described = getOwnPropertyDescriptor(object, keys[i]);
				if ("value" in described) {
					pending[waiting++] = described.value;
				} else {
					pending[waiting++] = described.get;
					pending[waiting++] = described.set;
				}
			}
			pending[waiting++] = getPrototypeOf(object);
		}
	};
})()`

var freezerProgram = sync.OnceValue(func() *goja.Program {
	return goja.MustCompile(freezerFile, intrinsicsFreezer, true)
})

func (r _Realm) intrinsicsFreezer() (goja.Callable, error) {
	made, err := r.vm.RunProgram(freezerProgram())
	if err != nil {
		return nil, fmt.Errorf("preparing to freeze the built-ins: %w", r.explain(err))
	}
	freezer, _ := goja.AssertFunction(made)
	return freezer, nil
}

func (s *_Session) freezeIntrinsics(freezer goja.Callable) error {
	return s.enter(func(r _Realm) error {
		if _, err := freezer(goja.Undefined(), r.vm.ToValue(readOnlyRefusal)); err != nil {
			return fmt.Errorf("freezing the built-ins: %w", r.explain(err))
		}
		return nil
	})
}
