package jaml

import "fmt"

type expressionError struct {
	kind  error
	cause error
}

func fail(kind error, format string, args ...any) error {
	return expressionError{kind: kind, cause: fmt.Errorf(format, args...)}
}

func (e expressionError) Error() string {
	return e.cause.Error() + ": " + e.kind.Error()
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
