package jaml

import "fmt"

const rootPlace = "$"

type _ExpressionError struct {
	kind  error
	path  string
	cause error
}

func fail(kind error, format string, args ...any) _ExpressionError {
	return _ExpressionError{kind: kind, cause: fmt.Errorf(format, args...)}
}

func (e _ExpressionError) within(path string) _ExpressionError {
	e.path = path
	return e
}

func (e _ExpressionError) place() string {
	if e.path == "" {
		return rootPlace
	}
	return e.path
}

func (e _ExpressionError) Error() string {
	return e.place() + e.joint() + e.cause.Error() + ": " + e.kind.Error()
}

func (e _ExpressionError) joint() string {
	if e.kind == MissingPath {
		return " "
	}
	return ": "
}

func (e _ExpressionError) Unwrap() []error {
	return []error{e.kind, e.cause}
}

type _MemoryExhausted string

func (m _MemoryExhausted) Error() string {
	return string(m)
}

func (m _MemoryExhausted) Is(target error) bool {
	return target == MemoryLimit
}
