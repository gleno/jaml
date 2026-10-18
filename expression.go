package jaml

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

const (
	wrapperOpens  = "(function(){\"use strict\"; return (\n"
	wrapperCloses = "\n)})"
)

var errEscapesItsWrapper = errors.New("write a single JavaScript expression")

type expression struct {
	source       string
	interpolated bool
	program      *goja.Program
}

func (e expression) written() string {
	if e.interpolated {
		return "${{" + preview(e.source) + "}}"
	}
	return "${" + preview(e.source) + "}"
}

func compileExpression(source string, interpolated bool) (expression, error) {
	parsed, err := parser.ParseFile(nil, "", wrapperOpens+source+wrapperCloses, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return expression{}, describeSyntaxError(source, err)
	}
	if !holdsOnlyTheWrappedExpression(parsed) {
		return expression{}, errEscapesItsWrapper
	}
	program, err := goja.CompileAST(parsed, true)
	if err != nil {
		return expression{}, describeSyntaxError(source, err)
	}
	return expression{source: source, interpolated: interpolated, program: program}, nil
}

func holdsOnlyTheWrappedExpression(parsed *ast.Program) bool {
	if len(parsed.Body) != 1 {
		return false
	}
	statement, isExpression := parsed.Body[0].(*ast.ExpressionStatement)
	if !isExpression {
		return false
	}
	wrapper, isFunction := statement.Expression.(*ast.FunctionLiteral)
	if !isFunction || wrapper.Body == nil || len(wrapper.Body.List) != 2 {
		return false
	}
	_, returns := wrapper.Body.List[1].(*ast.ReturnStatement)
	return returns
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
	line := at.Line - strings.Count(wrapperOpens, "\n")
	if line < 1 || line > strings.Count(source, "\n")+1 {
		return fmt.Errorf("syntax error at the end of the expression: %s", message)
	}
	return fmt.Errorf("syntax error at line %d, column %d of the expression: %s", line, at.Column, message)
}
