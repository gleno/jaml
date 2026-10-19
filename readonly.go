package jaml

import (
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/token"
)

const readOnlyRefusal = "cannot change a read-only value: values from WithValues and the built-ins are frozen, so change a copy instead, such as items.toSorted(), [...items].reverse() or { ...item, key: value }"

const (
	expressionFile = "jaml:expression"
	freezerFile    = "jaml:freezer"
)

var engineWriteRefusal = regexp.MustCompile(`^(|Cannot assign to read only property '.*'|Cannot add property .*, object is not extensible|Cannot set property .*, object is not extensible|Cannot delete property '.*' of .*|length is not writable|.* is not extensible)$`)

func (r realm) explainWrite(compiled expression, err error) error {
	var exception *goja.Exception
	if !errors.As(err, &exception) || !compiled.writesWhere(exception) || !r.refusesWrite(exception.Value()) {
		return err
	}
	return &Thrown{Message: "TypeError: " + readOnlyRefusal, Value: r.snapshot(exception.Value())}
}

func (r realm) refusesWrite(thrown goja.Value) bool {
	object, isObject := thrown.(*goja.Object)
	if !isObject || object.ExportType() == proxyType || object.ClassName() != "Error" {
		return false
	}
	var refuses bool
	if err := r.contain(func() {
		name, _ := primitiveField(object, "name")
		message, _ := primitiveField(object, "message")
		refuses = name == "TypeError" && !strings.HasSuffix(message, " of a String") && engineWriteRefusal.MatchString(message)
	}); err != nil {
		return false
	}
	return refuses
}

func (e expression) writesWhere(exception *goja.Exception) bool {
	stack := exception.Stack()
	if len(stack) == 0 || stack[0].SrcName() != expressionFile {
		return false
	}
	parsed, err := parseCode(e.source)
	if err != nil {
		return false
	}
	at := file.Idx(offsetOf(expressionOpens+e.source+"\n", stack[0].Position()) + 1)
	return anyNode(reflect.ValueOf(parsed.program), func(node any) bool {
		switch typed := node.(type) {
		case *ast.AssignExpression:
			return targetAt(typed.Left, at)
		case *ast.UnaryExpression:
			return changesOperand(typed.Operator) && targetAt(typed.Operand, at)
		}
		return false
	})
}

func changesOperand(operator token.Token) bool {
	return operator == token.DELETE || operator == token.INCREMENT || operator == token.DECREMENT
}

func targetAt(target ast.Expression, at file.Idx) bool {
	switch typed := target.(type) {
	case *ast.DotExpression:
		return typed.Identifier.Idx == at
	case *ast.BracketExpression:
		return typed.LeftBracket == at
	case *ast.ArrayPattern, *ast.ObjectPattern:
		return anyNode(reflect.ValueOf(typed), func(node any) bool {
			member, isMember := node.(ast.Expression)
			return isMember && member != target && (targetAt(member, at) || patternTargetAt(member, at))
		})
	}
	return false
}

func patternTargetAt(member ast.Expression, at file.Idx) bool {
	switch member.(type) {
	case *ast.DotExpression, *ast.BracketExpression:
		return member.Idx0() == at
	}
	return false
}

func anyNode(node reflect.Value, matches func(node any) bool) bool {
	switch node.Kind() {
	case reflect.Interface, reflect.Pointer:
		if node.IsNil() {
			return false
		}
		if node.Kind() == reflect.Pointer && matches(node.Interface()) {
			return true
		}
		return anyNode(node.Elem(), matches)
	case reflect.Slice:
		for i := range node.Len() {
			if anyNode(node.Index(i), matches) {
				return true
			}
		}
	case reflect.Struct:
		if node.Type().PkgPath() != reflect.TypeFor[ast.Program]().PkgPath() {
			return false
		}
		for i := range node.NumField() {
			if anyNode(node.Field(i), matches) {
				return true
			}
		}
	}
	return false
}
