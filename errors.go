package jaml

import "fmt"

const rootPlace = "$"

type expressionError struct {
	kind  error
	at    string
	cause error
}

func fail(kind error, format string, args ...any) expressionError {
	return expressionError{kind: kind, cause: fmt.Errorf(format, args...)}
}

func (e expressionError) within(at string) expressionError {
	e.at = at
	return e
}

func (e expressionError) place() string {
	if e.at == "" {
		return rootPlace
	}
	return e.at
}

func (e expressionError) Error() string {
	return e.place() + e.joint() + e.cause.Error() + ": " + e.kind.Error()
}

func (e expressionError) joint() string {
	if e.kind == MissingPath {
		return " "
	}
	return ": "
}

func (e expressionError) Unwrap() []error {
	return []error{e.kind, e.cause}
}

type memoryExhausted string

func (m memoryExhausted) Error() string {
	return string(m)
}

func (m memoryExhausted) Is(target error) bool {
	return target == MemoryLimit
}
