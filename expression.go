package jaml

import (
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

const expressionOpens = "\"use strict\";0,\n"

const closerStop = "Unexpected token }"

const maxCodeBytes = 64 << 10

const endSearchParsesPerByte = 32

var (
	errEmptyExpression  = errors.New("empty expression")
	errNotOneExpression = errors.New("syntax error: write a single JavaScript expression")
	errCostlyEnds       = fmt.Errorf("finding where its expressions end would parse more than %d times its length", endSearchParsesPerByte)
	errNotFunction      = errors.New("not a function literal: write an arrow function or a function expression")
	errAsyncFunction    = errors.New("an async function returns a promise, not data")
	errGenerator        = errors.New("a generator function returns an iterator, not data")
)

type _Expression struct {
	source  string
	opened  _Delimiter
	program *goja.Program
}

func (e _Expression) written() string {
	return e.opened.around(e.source)
}

type _ParsedCode struct {
	source  string
	program *ast.Program
}

func compileExpression(source string, opened _Delimiter) (_Expression, error) {
	parsed, err := parseCode(source)
	if err != nil {
		return _Expression{}, err
	}
	return parsed.compile(opened)
}

func parseCode(source string) (_ParsedCode, error) {
	if err := refuseLongCode(source); err != nil {
		return _ParsedCode{}, err
	}
	program, err := parseExpression(source)
	if err != nil {
		return _ParsedCode{}, err
	}
	return _ParsedCode{source: source, program: program}, nil
}

func (p _ParsedCode) compile(opened _Delimiter) (_Expression, error) {
	program, err := goja.CompileAST(p.program, true)
	if err != nil {
		return _Expression{}, describeSyntaxError(p.source, err)
	}
	return _Expression{source: p.source, opened: opened, program: program}, nil
}

func (p _ParsedCode) written() ast.Expression {
	statement, isStatement := p.program.Body[1].(*ast.ExpressionStatement)
	if !isStatement {
		return nil
	}
	sequence, isSequence := statement.Expression.(*ast.SequenceExpression)
	if !isSequence || len(sequence.Sequence) != 2 {
		return nil
	}
	return sequence.Sequence[1]
}

func (e _Expression) undefinedHint() string {
	parsed, err := parseCode(e.source)
	if err != nil || !endsInBracedArrow(parsed.written()) {
		return ""
	}
	return "; an arrow returning an object literal needs parentheses: () => ({ ... })"
}

func endsInBracedArrow(written ast.Expression) bool {
	arrow, isArrow := written.(*ast.ArrowFunctionLiteral)
	for isArrow {
		concise, isConcise := arrow.Body.(*ast.ExpressionBody)
		if !isConcise {
			return true
		}
		arrow, isArrow = concise.Expression.(*ast.ArrowFunctionLiteral)
	}
	return false
}

func (p _ParsedCode) refuseUnlessFunction() error {
	switch literal := p.written().(type) {
	case *ast.ArrowFunctionLiteral:
		if literal.Async {
			return errAsyncFunction
		}
		return nil
	case *ast.FunctionLiteral:
		switch {
		case literal.Async:
			return errAsyncFunction
		case literal.Generator:
			return errGenerator
		}
		return nil
	}
	return errNotFunction
}

func refuseLongCode(code string) error {
	if len(code) > maxCodeBytes {
		return fmt.Errorf("a value holding code is longer than %d bytes", maxCodeBytes)
	}
	return nil
}

func parseExpression(source string) (*ast.Program, error) {
	parsed, err := parseAfterOpens(source + "\n")
	switch {
	case err != nil && holdsNoCode(source):
		return nil, errEmptyExpression
	case err != nil:
		return nil, describeSyntaxError(source, err)
	case len(parsed.Body) != 2:
		return nil, errNotOneExpression
	}
	if literal := legacyOctalEscape(reflect.ValueOf(parsed.Body[1])); literal != nil {
		return nil, placeSyntaxError(source, parsed.File.Position(int(literal.Idx)-parsed.File.Base()), "Octal escape sequences are not allowed in strict mode")
	}
	return parsed, nil
}

func parseAfterOpens(code string) (*ast.Program, error) {
	return parser.ParseFile(nil, expressionFile, expressionOpens+code, 0, parser.WithDisableSourceMaps)
}

func holdsNoCode(source string) bool {
	parsed, err := parser.ParseFile(nil, "", source, 0, parser.WithDisableSourceMaps)
	return err == nil && len(parsed.Body) == 0
}

func legacyOctalEscape(node reflect.Value) *ast.StringLiteral {
	switch node.Kind() {
	case reflect.Interface, reflect.Pointer:
		if node.IsNil() {
			return nil
		}
		if literal, isString := node.Interface().(*ast.StringLiteral); isString {
			return escapesInLegacyOctal(literal)
		}
		return legacyOctalEscape(node.Elem())
	case reflect.Slice:
		for i := range node.Len() {
			if found := legacyOctalEscape(node.Index(i)); found != nil {
				return found
			}
		}
	case reflect.Struct:
		if node.Type().PkgPath() != reflect.TypeFor[ast.Program]().PkgPath() {
			return nil
		}
		for i := range node.NumField() {
			if found := legacyOctalEscape(node.Field(i)); found != nil {
				return found
			}
		}
	}
	return nil
}

func escapesInLegacyOctal(literal *ast.StringLiteral) *ast.StringLiteral {
	raw := literal.Literal
	for i := 0; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		escaped := raw[i]
		if '1' <= escaped && escaped <= '9' || escaped == '0' && i+1 < len(raw) && '0' <= raw[i+1] && raw[i+1] <= '9' {
			return literal
		}
	}
	return nil
}

type _ParseStop struct {
	offset  int
	at      file.Position
	message string
}

func (s _ParseStop) closes(after string, opened _Delimiter) bool {
	return s.message == closerStop && strings.HasPrefix(after[s.offset:], opened.closes)
}

type _EndSearch struct {
	unparsed int
}

func searchingEndsIn(value string) *_EndSearch {
	return &_EndSearch{unparsed: endSearchParsesPerByte * len(value)}
}

func (s *_EndSearch) expressionEnd(after string, opened _Delimiter) (int, error) {
	for window := range truncatedWindows(after, opened.closes) {
		if err := s.spend(window); err != nil {
			return 0, err
		}
		if stop, failed := firstParseError(after[:window]); failed && stop.closes(after, opened) {
			return stop.offset, nil
		}
	}
	if err := s.spend(len(after)); err != nil {
		return 0, err
	}
	return finalEnd(after, opened)
}

func (s *_EndSearch) spend(parsed int) error {
	if parsed > s.unparsed {
		return errCostlyEnds
	}
	s.unparsed -= parsed
	return nil
}

func finalEnd(after string, opened _Delimiter) (int, error) {
	stop, failed := firstParseError(after)
	switch {
	case failed && strings.HasPrefix(after[stop.offset:], opened.closes):
		return stop.offset, nil
	case !failed || strings.HasPrefix(opened.closes, after[stop.offset:]):
		return 0, fmt.Errorf("unclosed %s", opened.opens)
	}
	return 0, placeSyntaxError(after, stop.at, stop.message)
}

func truncatedWindows(after, closer string) iter.Seq[int] {
	return func(yield func(int) bool) {
		seen, pending := 0, 0
		for at := range occurrences(after, closer) {
			if pending > 0 && !yield(pending) {
				return
			}
			seen++
			pending = 0
			if seen&(seen-1) == 0 {
				pending = at + len(closer)
			}
		}
	}
}

func occurrences(text, closer string) iter.Seq[int] {
	return func(yield func(int) bool) {
		for at := strings.Index(text, closer); at >= 0; {
			if !yield(at) {
				return
			}
			next := strings.Index(text[at+1:], closer)
			if next < 0 {
				return
			}
			at += 1 + next
		}
	}
}

func firstParseError(code string) (_ParseStop, bool) {
	_, err := parseAfterOpens(code)
	var parseErrors parser.ErrorList
	if !errors.As(err, &parseErrors) || len(parseErrors) == 0 {
		return _ParseStop{}, false
	}
	first := parseErrors[0]
	offset := offsetOf(expressionOpens+code, first.Position) - len(expressionOpens)
	return _ParseStop{offset: max(offset, 0), at: first.Position, message: first.Message}, true
}

func offsetOf(program string, at file.Position) int {
	located := file.NewFile("", program, 1)
	return sort.Search(len(program)+1, func(offset int) bool {
		here := located.Position(offset)
		return here.Line > at.Line || here.Line == at.Line && here.Column >= at.Column
	})
}

func describeSyntaxError(source string, err error) error {
	var parseErrors parser.ErrorList
	if errors.As(err, &parseErrors) && len(parseErrors) > 0 {
		return placeSyntaxError(source, parseErrors[0].Position, parseErrors[0].Message)
	}
	var compileError *goja.CompilerSyntaxError
	if errors.As(err, &compileError) && compileError.File != nil {
		return placeSyntaxError(source, compileError.File.Position(compileError.Offset), compileError.Message)
	}
	return err
}

func placeSyntaxError(source string, at file.Position, message string) error {
	return syntaxErrorAt(source, offsetOf(expressionOpens+source, at)-len(expressionOpens), message)
}

func syntaxErrorAt(source string, offset int, message string) error {
	reason := excerpt(message, thrownExcerptLength)
	switch {
	case offset < 0:
		return fmt.Errorf("syntax error in the expression: %s", reason)
	case offset >= len(source) || strings.TrimSpace(source[offset:]) == "":
		return fmt.Errorf("syntax error at the end of the expression: %s", reason)
	}
	before := source[:offset]
	line := strings.Count(before, "\n") + 1
	column := utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:]) + 1
	return fmt.Errorf("syntax error at line %d, column %d of the expression: %s at %q", line, column, reason, excerpt(source[offset:], codeExcerptLength))
}
