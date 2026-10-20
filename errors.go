package jaml

import (
	"fmt"
	"time"
)

const rootPlace = "$"

type Failure struct {
	Path    string
	Reason  string
	printed string
}

func (f *Failure) Error() string {
	if f.printed != "" {
		return f.printed
	}
	return f.Path + ": " + f.Reason
}

type _Unplaced struct {
	kind  _Kind
	label string
	cause error
}

type _ExpressionError struct {
	unplaced _Unplaced
	path     string
}

type _LabelledCause struct {
	label string
	cause error
}

func (l _LabelledCause) Error() string {
	return _Unplaced{label: l.label, cause: l.cause}.parts()
}

func (l _LabelledCause) Unwrap() error {
	return l.cause
}

func fail(kind _Kind, format string, args ...any) _Unplaced {
	return failWith(kind, fmt.Errorf(format, args...))
}

func failWith(kind _Kind, err error) _Unplaced {
	if labelled, isLabelled := err.(_LabelledCause); isLabelled {
		return _Unplaced{kind: kind, label: labelled.label, cause: labelled.cause}
	}
	return _Unplaced{kind: kind, cause: err}
}

func (u _Unplaced) at(path string) _ExpressionError {
	return _ExpressionError{unplaced: u, path: path}
}

func (u _Unplaced) Error() string {
	return u.parts() + ": " + u.kind.sentinel().Error()
}

func (u _Unplaced) parts() string {
	if u.label == "" {
		return u.cause.Error()
	}
	return u.label + ": " + u.cause.Error()
}

func (u _Unplaced) Unwrap() []error {
	return []error{u.kind.sentinel(), u.cause}
}

func (e _ExpressionError) Error() string {
	return e.path + e.joint() + e.unplaced.Error()
}

func (e _ExpressionError) As(target any) bool {
	failure, wanted := target.(**Failure)
	if wanted {
		*failure = &Failure{Path: e.path, Reason: e.unplaced.cause.Error(), printed: e.Error()}
	}
	return wanted
}

func (e _ExpressionError) joint() string {
	if e.unplaced.kind == missingPath {
		return " "
	}
	return ": "
}

func (e _ExpressionError) Unwrap() []error {
	return e.unplaced.Unwrap()
}

func located(path string, err error) _ExpressionError {
	switch failed := err.(type) {
	case _ExpressionError:
		return failed
	case _Unplaced:
		return failed.at(path)
	}
	return failWith(evaluationFailed, err).at(path)
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
