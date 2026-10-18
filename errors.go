package jaml

import "fmt"

type templateError struct {
	kind  error
	cause error
}

func fail(kind error, format string, args ...any) error {
	return templateError{kind: kind, cause: fmt.Errorf(format, args...)}
}

func (e templateError) Error() string {
	return e.cause.Error() + ": " + e.kind.Error()
}

func (e templateError) Unwrap() []error {
	return []error{e.kind, e.cause}
}

type memoryExhausted string

func (m memoryExhausted) Error() string {
	return string(m)
}

func (m memoryExhausted) Is(target error) bool {
	return target == MemoryLimit
}
