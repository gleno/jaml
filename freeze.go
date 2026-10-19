package jaml

import (
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

const intrinsicsFreezer = `(() => {
	"use strict";
	const { freeze, isFrozen, getOwnPropertyDescriptor, getPrototypeOf, defineProperty } = Object;
	const ownKeys = Reflect.ownKeys;
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
	return () => {
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
	return goja.MustCompile("", intrinsicsFreezer, true)
})

func (r realm) intrinsicsFreezer() (goja.Callable, error) {
	made, err := r.vm.RunProgram(freezerProgram())
	if err != nil {
		return nil, fmt.Errorf("preparing to freeze the built-ins: %w", r.explain(err))
	}
	freezer, _ := goja.AssertFunction(made)
	return freezer, nil
}

func (s *session) freezeIntrinsics(freezer goja.Callable) error {
	return s.enter(func(r realm) error {
		if _, err := freezer(goja.Undefined()); err != nil {
			return fmt.Errorf("freezing the built-ins: %w", r.explain(err))
		}
		return nil
	})
}
