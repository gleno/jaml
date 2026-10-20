package jaml

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/token"
)

const (
	readOnlyRefusal = "cannot change a read-only value: values from WithValues and the built-ins are frozen, so change a copy instead, such as items.toSorted(), [...items].reverse() or { ...item, key: value }"
	nameRefusal     = "cannot change a read-only value: a name from WithValues cannot be assigned, so change the value in place or keep the new one under another name"
)

const (
	expressionFile = "jaml:expression"
)

var engineWriteRefusal = regexp.MustCompile(`^(|Cannot assign to read only property '.*'|Cannot add property .*, object is not extensible|Cannot set property .*, object is not extensible|Cannot delete property '.*' of .*|Cannot redefine property: .*|length is not writable|.* is not extensible)$`)

func (r _Realm) explainWrite(compiled _Expression, err error) error {
	var exception *goja.Exception
	if !errors.As(err, &exception) || !compiled.writesWhere(exception) || !r.refusesWrite(exception.Value()) {
		return err
	}
	if refused := r.protection.refuseWrite(r, exception.Value()); refused != nil {
		return refused
	}
	return err
}

func (r _Realm) refusesWrite(thrown goja.Value) bool {
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

func (e _Expression) writesWhere(exception *goja.Exception) bool {
	var stack = exception.Stack()
	if len(stack) == 0 || stack[0].SrcName() != expressionFile {
		return false
	}
	var offset = file.Idx(offsetOf(expressionOpens+e.source+"\n", stack[0].Position()) + 1)
	return slices.ContainsFunc(e.writes, func(site _WriteSite) bool { return site.from <= offset && offset < site.to })
}

type _WriteSite struct {
	from file.Idx
	to   file.Idx
}

func exactly(offset file.Idx) _WriteSite {
	return _WriteSite{from: offset, to: offset + 1}
}

func writeSites(program *ast.Program) []_WriteSite {
	var sites []_WriteSite
	anyNode(reflect.ValueOf(program), func(node any) bool {
		switch typed := node.(type) {
		case *ast.AssignExpression:
			sites = append(sites, targetsOf(typed.Left)...)
			if _, named := typed.Left.(*ast.Identifier); named {
				sites = append(sites, _WriteSite{from: typed.Idx0(), to: typed.Idx1()})
			}
		case *ast.UnaryExpression:
			if changesOperand(typed.Operator) {
				sites = append(sites, targetsOf(typed.Operand)...)
			}
		}
		return false
	})
	return sites
}

func changesOperand(operator token.Token) bool {
	return operator == token.DELETE || operator == token.INCREMENT || operator == token.DECREMENT
}

func targetsOf(target ast.Expression) []_WriteSite {
	switch typed := target.(type) {
	case *ast.DotExpression:
		return []_WriteSite{exactly(typed.Identifier.Idx)}
	case *ast.BracketExpression:
		return []_WriteSite{exactly(typed.LeftBracket)}
	case *ast.ArrayPattern, *ast.ObjectPattern:
		var sites []_WriteSite
		anyNode(reflect.ValueOf(typed), func(node any) bool {
			if member, isMember := node.(ast.Expression); isMember && member != target {
				sites = append(sites, targetsOf(member)...)
				sites = append(sites, patternTargetsOf(member)...)
			}
			return false
		})
		return sites
	}
	return nil
}

func patternTargetsOf(member ast.Expression) []_WriteSite {
	switch member.(type) {
	case *ast.DotExpression, *ast.BracketExpression:
		return []_WriteSite{exactly(member.Idx0())}
	}
	return nil
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
