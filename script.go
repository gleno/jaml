package jaml

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

const (
	scriptOpens       = "\"use strict\";\n"
	wrappedOpens      = "\"use strict\";\n(() => {"
	maxCachedScripts  = 128
	scriptValueTarget = "value"
)

type _Script struct {
	program *goja.Program
	names   []string
}

type _ScriptCache struct {
	mu       sync.Mutex
	compiled map[string]_Script
}

var scripts = &_ScriptCache{compiled: map[string]_Script{}}

func (c *_ScriptCache) compile(source string) (_Script, error) {
	c.mu.Lock()
	cached, found := c.compiled[source]
	c.mu.Unlock()
	if found {
		return cached, nil
	}
	compiled, err := compileScript(source)
	if err != nil {
		return _Script{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.compiled) >= maxCachedScripts {
		clear(c.compiled)
	}
	c.compiled[source] = compiled
	return compiled, nil
}

func compileScript(source string) (compiled _Script, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = failure
		}
	}()
	program, err := parser.ParseFile(nil, "", scriptOpens+source, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return _Script{}, placeScriptError(source, scriptOpens, err)
	}
	var names = declaredNames(program)
	var wrapped = wrappedOpens + source + "\n;return [" + exports(names) + "]})"
	parsed, err := parser.ParseFile(nil, "", wrapped, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return _Script{}, placeScriptError(source, wrappedOpens, err)
	}
	compiledProgram, err := goja.CompileAST(parsed, true)
	if err != nil {
		return _Script{}, placeScriptError(source, wrappedOpens, err)
	}
	return _Script{program: compiledProgram, names: names}, nil
}

func placeScriptError(source, opens string, err error) error {
	var parseErrors parser.ErrorList
	var compileError *goja.CompilerSyntaxError
	switch {
	case errors.As(err, &parseErrors) && len(parseErrors) > 0:
		return syntaxErrorAt(source, offsetOf(opens+source, parseErrors[0].Position)-len(opens), parseErrors[0].Message)
	case errors.As(err, &compileError) && compileError.File != nil:
		return syntaxErrorAt(source, offsetOf(opens+source, compileError.File.Position(compileError.Offset))-len(opens), compileError.Message)
	}
	return err
}

func declaredNames(program *ast.Program) []string {
	var names []string
	for _, declaration := range program.DeclarationList {
		for _, binding := range declaration.List {
			names = boundNames(binding.Target, names)
		}
	}
	for _, statement := range program.Body {
		switch typed := statement.(type) {
		case *ast.LexicalDeclaration:
			for _, binding := range typed.List {
				names = boundNames(binding.Target, names)
			}
		case *ast.FunctionDeclaration:
			names = append(names, typed.Function.Name.Name.String())
		case *ast.ClassDeclaration:
			names = append(names, typed.Class.Name.Name.String())
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func boundNames(target ast.BindingTarget, names []string) []string {
	switch typed := target.(type) {
	case *ast.Identifier:
		return append(names, typed.Name.String())
	case *ast.ArrayPattern:
		for _, element := range typed.Elements {
			names = patternNames(element, names)
		}
		return patternNames(typed.Rest, names)
	case *ast.ObjectPattern:
		for _, property := range typed.Properties {
			switch held := property.(type) {
			case *ast.PropertyShort:
				names = append(names, held.Name.Name.String())
			case *ast.PropertyKeyed:
				names = patternNames(held.Value, names)
			}
		}
		return patternNames(typed.Rest, names)
	}
	return names
}

func patternNames(node ast.Expression, names []string) []string {
	switch typed := node.(type) {
	case nil:
		return names
	case *ast.AssignExpression:
		return patternNames(typed.Left, names)
	case ast.BindingTarget:
		return boundNames(typed, names)
	}
	return names
}

func exports(names []string) string {
	var target = scriptValueTarget
	for slices.Contains(names, target) {
		target += "_"
	}
	var written []string
	for _, name := range names {
		written = append(written, fmt.Sprintf("() => %s, (%s) => { %s = %s; }", name, target, name, target))
	}
	return strings.Join(written, ", ")
}

func (s *_Session) run(sources []string, values _BoundValues) error {
	for index, source := range sources {
		compiled, err := scripts.compile(source)
		if err != nil {
			return fail(InvalidExpression, "script %d: %w", index+1, err)
		}
		if err := s.enter(func(r _Realm) error { return r.runScript(compiled, values) }); err != nil {
			return fail(EvaluationFailed, "script %d: %w", index+1, err)
		}
	}
	return nil
}

func (r _Realm) runScript(compiled _Script, values _BoundValues) error {
	wrapper, err := r.vm.RunProgram(compiled.program)
	if err != nil {
		return r.explain(err)
	}
	run, _ := goja.AssertFunction(wrapper)
	returned, err := run(r.vm.GlobalObject())
	if err != nil {
		return r.explain(err)
	}
	var accessors = returned.(*goja.Object)
	for index, name := range compiled.names {
		if _, shadowed := values[name]; shadowed {
			continue
		}
		getter, setter := accessors.Get(fmt.Sprint(2*index)), accessors.Get(fmt.Sprint(2*index+1))
		if err := r.vm.GlobalObject().DefineAccessorProperty(name, getter, setter, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
			return fmt.Errorf("cannot declare the global %q: %w", clip(name, keyExcerptLength), r.explain(err))
		}
	}
	return nil
}
