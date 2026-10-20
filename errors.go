package jaml

import (
	"fmt"
	"time"
)

const rootPlace = "$"

type Failure struct {
	Path   string
	Reason string
}

func (f *Failure) Error() string {
	return f.Path + ": " + f.Reason
}

type _ExpressionError struct {
	kind  error
	path  string
	code  string
	cause error
}

type _Quoted struct {
	code   string
	reason error
}

func (q _Quoted) Error() string {
	return q.code + ": " + q.reason.Error()
}

func (q _Quoted) Unwrap() error {
	return q.reason
}

func fail(kind error, format string, args ...any) _ExpressionError {
	return failWith(kind, fmt.Errorf(format, args...))
}

func failWith(kind error, err error) _ExpressionError {
	if quoted, isQuoted := err.(_Quoted); isQuoted {
		return _ExpressionError{kind: kind, code: quoted.code, cause: quoted.reason}
	}
	return _ExpressionError{kind: kind, cause: err}
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
	return e.place() + e.joint() + e.quoted() + e.cause.Error() + ": " + e.kind.Error()
}

func (e _ExpressionError) quoted() string {
	if e.code == "" {
		return ""
	}
	return e.code + ": "
}

func (e _ExpressionError) As(target any) bool {
	failure, wanted := target.(**Failure)
	if wanted {
		*failure = &Failure{Path: e.place(), Reason: e.cause.Error()}
	}
	return wanted
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

type _TimeExhausted struct {
	limit time.Duration
}

func (t _TimeExhausted) Error() string {
	return fmt.Sprintf("exceeded the time limit of %s", t.limit)
}

func (t _TimeExhausted) Is(target error) bool {
	return target == TimeLimit
}
