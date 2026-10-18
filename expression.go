package jaml

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

const expressionOpens = "\"use strict\";0,\n"

const closerStop = "Unexpected token }"

const maxCodeBytes = 64 << 10

var (
	errEmptyExpression  = errors.New("empty expression")
	errNotOneExpression = errors.New("syntax error: write a single JavaScript expression")
)

type expression struct {
	id           int
	source       string
	interpolated bool
	program      *goja.Program
}

func (e expression) written() string {
	if e.interpolated {
		return interpolation.around(e.source)
	}
	return wholeValue.around(e.source)
}

func compileExpression(source string, interpolated bool) (expression, error) {
	if err := refuseLongCode(source); err != nil {
		return expression{}, err
	}
	parsed, err := parseExpression(source)
	if err != nil {
		return expression{}, err
	}
	program, err := goja.CompileAST(parsed, true)
	if err != nil {
		return expression{}, describeSyntaxError(source, err)
	}
	return expression{source: source, interpolated: interpolated, program: program}, nil
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
	return parser.ParseFile(nil, "", expressionOpens+code, 0, parser.WithDisableSourceMaps)
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

type parseStop struct {
	offset  int
	at      file.Position
	message string
}

func (s parseStop) closes(after string, opened delimiter) bool {
	return s.message == closerStop && strings.HasPrefix(after[s.offset:], opened.closes)
}

func expressionEnd(after string, opened delimiter) (int, error) {
	closers := occurrences(after, opened.closes)
	for reach := 1; reach < len(closers); reach *= 2 {
		window := after[:closers[reach-1]+len(opened.closes)]
		if stop, failed := firstParseError(window); failed && stop.closes(after, opened) {
			return stop.offset, nil
		}
	}
	return finalEnd(after, opened)
}

func finalEnd(after string, opened delimiter) (int, error) {
	stop, failed := firstParseError(after)
	switch {
	case failed && stop.closes(after, opened):
		return stop.offset, nil
	case !failed || strings.HasPrefix(opened.closes, after[stop.offset:]):
		return 0, fmt.Errorf("unclosed %s", opened.opens)
	}
	return 0, placeSyntaxError(after, stop.at, stop.message)
}

func occurrences(text, closer string) []int {
	var found []int
	for at := strings.Index(text, closer); at >= 0; {
		found = append(found, at)
		next := strings.Index(text[at+1:], closer)
		if next < 0 {
			break
		}
		at += 1 + next
	}
	return found
}

func firstParseError(code string) (parseStop, bool) {
	_, err := parseAfterOpens(code)
	var parseErrors parser.ErrorList
	if !errors.As(err, &parseErrors) || len(parseErrors) == 0 {
		return parseStop{}, false
	}
	first := parseErrors[0]
	offset := offsetOf(expressionOpens+code, first.Position) - len(expressionOpens)
	return parseStop{offset: max(offset, 0), at: first.Position, message: first.Message}, true
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
	line := at.Line - strings.Count(expressionOpens, "\n")
	switch {
	case line < 1:
		return fmt.Errorf("syntax error in the expression: %s", message)
	case line > strings.Count(source, "\n")+1:
		return fmt.Errorf("syntax error at the end of the expression: %s", message)
	}
	return fmt.Errorf("syntax error at line %d, column %d of the expression: %s", line, at.Column, message)
}
