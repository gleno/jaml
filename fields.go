package jaml

import (
	"reflect"
	"strings"
)

type structField struct {
	index []int
	depth int
}

func fieldsOf(structType reflect.Type) map[string]structField {
	fields := map[string]structField{}
	collectFields(structType, nil, fields)
	return fields
}

func collectFields(structType reflect.Type, parent []int, fields map[string]structField) {
	for i := range structType.NumField() {
		declared := structType.Field(i)
		index := append(append([]int(nil), parent...), i)
		name, skip := jsonName(declared)
		if skip {
			continue
		}
		if declared.Anonymous && name == "" {
			embedded := declared.Type
			if embedded.Kind() == reflect.Pointer {
				if !declared.IsExported() {
					continue
				}
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				collectFields(embedded, index, fields)
				continue
			}
		}
		if !declared.IsExported() {
			continue
		}
		if name == "" {
			name = declared.Name
		}
		existing, taken := fields[name]
		if taken && existing.depth <= len(parent) {
			continue
		}
		fields[name] = structField{index: index, depth: len(parent)}
	}
}

func jsonName(declared reflect.StructField) (string, bool) {
	tag, tagged := declared.Tag.Lookup("json")
	if !tagged {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" && !strings.Contains(tag, ",") {
		return "", true
	}
	return name, false
}

func fieldByIndexAllocating(structValue reflect.Value, index []int) reflect.Value {
	current := structValue
	for depth, i := range index {
		if depth > 0 && current.Kind() == reflect.Pointer {
			if current.IsNil() {
				current.Set(reflect.New(current.Type().Elem()))
			}
			current = current.Elem()
		}
		current = current.Field(i)
	}
	return current
}
