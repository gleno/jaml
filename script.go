package jaml

import (
	"container/list"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
	"github.com/dop251/goja/unistring"
)

const (
	scriptOpens         = "\"use strict\";\n"
	scriptValueTarget   = "value"
	trustedScriptBudget = 32 << 20
	compiledPerSource   = 16
	compiledPerName     = 768
)

type _Script struct {
	program *goja.Program
	names   []string
}

type _ScriptCache struct {
	mu      sync.Mutex
	budget  int64
	held    int64
	recent  *list.List
	entries map[string]*list.Element
}

type _CachedScript struct {
	source string
	script _Script
	err    error
}

var scripts = newScriptCache(trustedScriptBudget)

func newScriptCache(budget int64) *_ScriptCache {
	return &_ScriptCache{budget: budget, recent: list.New(), entries: map[string]*list.Element{}}
}

func (c *_ScriptCache) compile(source string) (_Script, error) {
	if cached := c.recall(source); cached != nil {
		return cached.script, cached.err
	}
	compiled, err := compileScript(source)
	c.keep(&_CachedScript{source: source, script: compiled, err: err})
	return compiled, err
}

func (c *_ScriptCache) recall(source string) *_CachedScript {
	c.mu.Lock()
	defer c.mu.Unlock()
	var entry = c.entries[source]
	if entry == nil {
		return nil
	}
	c.recent.MoveToFront(entry)
	return entry.Value.(*_CachedScript)
}

func (c *_ScriptCache) keep(cached *_CachedScript) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached.weight() > c.budget || c.entries[cached.source] != nil {
		return
	}
	for c.held+cached.weight() > c.budget {
		var oldest = c.recent.Remove(c.recent.Back()).(*_CachedScript)
		delete(c.entries, oldest.source)
		c.held -= oldest.weight()
	}
	c.entries[cached.source] = c.recent.PushFront(cached)
	c.held += cached.weight()
}

func scriptBudget(limit _MemoryLimit) int64 {
	return int64(limit) / 8
}

func (c *_CachedScript) weight() int64 {
	if c.err != nil {
		return int64(len(c.source) + len(c.err.Error()))
	}
	return int64(len(c.source))*compiledPerSource + int64(len(c.script.names))*compiledPerName
}

func compileScript(source string) (compiled _Script, err error) {
	defer func() {
		if failure := buildPanic(recover()); failure != nil {
			err = failure
		}
	}()
	if err := refuseDeepScript(source); err != nil {
		return _Script{}, err
	}
	program, err := parser.ParseFile(nil, "", scriptOpens+source, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return _Script{}, placeScriptError(source, scriptOpens, err)
	}
	var names = declaredNames(program)
	compiledProgram, err := goja.CompileAST(exporting(program, names), true)
	if err != nil {
		return _Script{}, placeScriptError(source, scriptOpens, err)
	}
	return _Script{program: compiledProgram, names: names}, nil
}

func exporting(program *ast.Program, names []string) *ast.Program {
	var directive = program.Body[0]
	var end = file.Idx(program.File.Base() + len(program.File.Source()))
	var exported = &ast.ReturnStatement{Return: end, Argument: &ast.ArrayLiteral{LeftBracket: end, RightBracket: end, Value: accessors(names, end)}}
	var run = &ast.ArrowFunctionLiteral{
		Start:           directive.Idx1(),
		ParameterList:   &ast.ParameterList{Opening: directive.Idx1(), Closing: directive.Idx1()},
		Body:            &ast.BlockStatement{LeftBrace: directive.Idx1(), List: append(slices.Clip(program.Body[1:]), exported), RightBrace: end},
		DeclarationList: program.DeclarationList,
	}
	return &ast.Program{Body: []ast.Statement{directive, &ast.ExpressionStatement{Expression: run}}, File: program.File}
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

func accessors(names []string, place file.Idx) []ast.Expression {
	var target = scriptValueTarget
	for slices.Contains(names, target) {
		target += "_"
	}
	var named = func(name string) *ast.Identifier {
		return &ast.Identifier{Name: unistring.NewFromString(name), Idx: place}
	}
	var arrow = func(parameters []*ast.Binding, body ast.Expression) *ast.ArrowFunctionLiteral {
		return &ast.ArrowFunctionLiteral{Start: place, ParameterList: &ast.ParameterList{Opening: place, List: parameters, Closing: place}, Body: &ast.ExpressionBody{Expression: body}}
	}
	var written []ast.Expression
	for _, name := range names {
		var assignment = &ast.AssignExpression{Operator: token.ASSIGN, Left: named(name), Right: named(target)}
		written = append(written, arrow(nil, named(name)), arrow([]*ast.Binding{{Target: named(target)}}, assignment))
	}
	return written
}

func (s *_Session) run(sources []string, values _BoundValues) error {
	for index, source := range sources {
		compiled, err := scripts.compile(source)
		if err != nil {
			return _Unplaced{kind: invalidExpression, label: scriptLabel(index), cause: err}
		}
		if err := s.enter(func(r _Realm) error { return r.runScript(compiled, values) }); err != nil {
			return _Unplaced{kind: evaluationFailed, label: scriptLabel(index), cause: err}
		}
	}
	return nil
}

func scriptLabel(index int) string {
	return fmt.Sprintf("script %d", index+1)
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
