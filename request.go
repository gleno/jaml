package jaml

import (
	"context"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	requestHeadroom = 128
	requestEnvelope = requestHeadroom - frameHeader
	unboundedAsk    = math.MaxInt - requestHeadroom - 1
	bindingValues   = "binding values"
)

type _Ask struct {
	path      []string
	values    map[string]any
	scripts   []string
	args      []any
	skips     *_Shape
	pinned    _Pinned
	wallLimit _WallLimit
	timeLimit _TimeLimit
	codeFree  bool
	console   *_Console
}

type _RequestBody struct {
	opens    string
	op       _Opcode
	written  []byte
	limit    _WallLimit
	compiles bool
}

func (b *_RequestBody) overseen(ctx context.Context, buildTimeout time.Duration) *_Oversight {
	if b.compiles {
		return b.limit.overseen(ctx, buildTimeout, 2*cancelGrace)
	}
	return b.limit.overseen(ctx, cancelGrace)
}

type _Outgoing struct {
	unit    uint64
	body    *_RequestBody
	console *_Console
}

type _Span struct {
	start int
	end   int
}

type _WrittenWork struct {
	ask    _Ask
	names  []string
	values []_Span
	args   []_Span
	body   *_RequestBody
}

type _EncoderSink struct {
	w *_WireWriter
}

type _InvalidText struct{ cause error }

func (_InvalidText) Error() string       { return errInvalidText.Error() }
func (_InvalidText) Is(other error) bool { return other == errInvalidText }
func (e _InvalidText) Unwrap() error     { return e.cause }

type _InputLimit struct{ part string }

func (l _InputLimit) Error() string { return "while writing " + l.part }
func (l _InputLimit) Unwrap() error { return _PastLimit{} }

func inputPart(part string, err error) error {
	if errors.As(err, new(_InputLimit)) || !errors.As(err, new(_PastLimit)) {
		return err
	}
	return _InputLimit{part: part}
}

func bodyWriter(limit int) *_WireWriter {
	return &_WireWriter{written: make([]byte, requestHeadroom), limit: requestHeadroom + min(limit, unboundedAsk)}
}

func writeWork(ask _Ask, limit int) (_WrittenWork, error) {
	var w = bodyWriter(limit)
	var written = _WrittenWork{ask: ask, names: slices.Sorted(maps.Keys(ask.values))}
	var err = written.fill(w)
	if errors.As(err, new(_PastLimit)) {
		var at = _InputLimit{part: "the request metadata"}
		_ = errors.As(err, &at)
		return _WrittenWork{}, fail(invalidInput, "the request is larger than %d bytes %s", limit, at).at(rootPlace)
	}
	if err != nil {
		return _WrittenWork{}, located(rootPlace, err)
	}
	w.mark('}')
	written.body = &_RequestBody{opens: `"op":"serve","work":`, op: opServe, written: w.written, limit: ask.wallLimit, compiles: len(ask.scripts) > 0}
	return written, nil
}

func (ww *_WrittenWork) fill(w *_WireWriter) error {
	if err := w.pinned(ww.ask.pinned); err != nil {
		return err
	}
	if ww.ask.wallLimit != 0 {
		if err := w.raw(`,"wall":` + strconv.FormatInt(int64(ww.ask.wallLimit), 10)); err != nil {
			return err
		}
	}
	if ww.ask.timeLimit != 0 {
		if err := w.raw(`,"limit":` + strconv.FormatInt(int64(ww.ask.timeLimit), 10)); err != nil {
			return err
		}
	}
	if ww.ask.console != nil {
		if err := w.raw(`,"console":true`); err != nil {
			return err
		}
	}
	if len(ww.ask.path) > 0 {
		if err := w.strings(`,"path":`, ww.ask.path); err != nil {
			return inputPart("the path", err)
		}
	}
	if err := ww.fillValues(w); err != nil {
		return inputPart("the values", err)
	}
	scripts, err := admitScripts(ww.ask.scripts)
	if err != nil {
		return err
	}
	if len(scripts) > 0 {
		if err := w.strings(`,"scripts":`, scripts); err != nil {
			return inputPart("the scripts", err)
		}
	}
	if err := ww.fillArgs(w); err != nil {
		return inputPart("the arguments", err)
	}
	if ww.ask.skips != nil {
		if err := w.raw(`,"skips":`); err != nil {
			return inputPart("the result shape", err)
		}
		if err := w.marshalled(ww.ask.skips); err != nil {
			return inputPart("the result shape", err)
		}
	}
	return w.raw("}")
}

func (ww *_WrittenWork) fillValues(w *_WireWriter) error {
	if ww.ask.codeFree {
		return ww.checkValues()
	}
	if len(ww.names) == 0 {
		return nil
	}
	if err := w.raw(`,"values":{`); err != nil {
		return err
	}
	for index, name := range ww.names {
		if err := admitName(name); err != nil {
			return _Unplaced{kind: invalidInput, label: bindingValues, cause: err}
		}
		if index > 0 {
			w.mark(',')
		}
		var named = fmt.Sprintf("value %q", clip(name, keyExcerptLength))
		if err := w.quoted(name); err != nil {
			return inputPart(named, err)
		}
		w.mark(':')
		span, err := w.admitted(named, ww.ask.values[name])
		if err != nil {
			return bound(bindingValues, inputPart(named, err))
		}
		ww.values = append(ww.values, span)
	}
	return w.raw("}")
}

func (ww *_WrittenWork) checkValues() error {
	for _, name := range ww.names {
		if err := admitName(name); err != nil {
			return _Unplaced{kind: invalidInput, label: bindingValues, cause: err}
		}
		if _, err := unboundedWriter().admitted(fmt.Sprintf("value %q", clip(name, keyExcerptLength)), ww.ask.values[name]); err != nil {
			return bound(bindingValues, err)
		}
	}
	ww.names = nil
	return nil
}

func (ww *_WrittenWork) fillArgs(w *_WireWriter) error {
	if len(ww.ask.args) == 0 {
		return nil
	}
	if err := w.raw(`,"args":[`); err != nil {
		return err
	}
	for index, arg := range ww.ask.args {
		if index > 0 {
			w.mark(',')
		}
		var named = fmt.Sprintf("argument %d", index+1)
		span, err := w.admitted(named, arg)
		if err != nil {
			return bound("binding arguments", inputPart(named, err))
		}
		ww.args = append(ww.args, span)
	}
	return w.raw("]")
}

func bound(binding string, err error) error {
	if errors.As(err, new(_PastLimit)) {
		return err
	}
	return _Unplaced{kind: invalidInput, label: binding, cause: err}
}

func (ww _WrittenWork) view() _Work {
	var values = make(_BoundValues, len(ww.names))
	for index, name := range ww.names {
		values[name] = ww.datum(ww.values[index])
	}
	var args = make([]_Datum, len(ww.args))
	for index, span := range ww.args {
		args[index] = ww.datum(span)
	}
	return _Work{Path: ww.ask.path, Values: values, Scripts: ww.ask.scripts, Args: args, Skips: ww.ask.skips, Pinned: ww.ask.pinned, WallLimit: ww.ask.wallLimit, Console: ww.ask.console != nil}
}

func (ww _WrittenWork) datum(span _Span) _Datum {
	return _Datum{encoded: ww.body.written[span.start:span.end:span.end]}
}

func writePlan(plan _Blueprint, limit int) (*_RequestBody, error) {
	var w = bodyWriter(limit)
	if err := w.plan(plan); err != nil {
		if errors.As(err, new(_PastLimit)) {
			return nil, fail(invalidExpression, "the request is larger than %d bytes while writing the document", limit)
		}
		return nil, err
	}
	w.mark('}')
	return &_RequestBody{opens: `"op":"build","plan":`, op: opBuild, written: w.written}, nil
}

func (w *_WireWriter) plan(plan _Blueprint) error {
	if err := w.labelled(`{"written":`, plan.Written); err != nil {
		return err
	}
	if err := w.raw(`,"null":` + strconv.Itoa(int(plan.Null)) + `,"undefined":` + strconv.Itoa(int(plan.Undefined))); err != nil {
		return err
	}
	if plan.Raw {
		if err := w.raw(`,"raw":true`); err != nil {
			return err
		}
	}
	if plan.Function {
		if err := w.raw(`,"function":true`); err != nil {
			return err
		}
	}
	if plan.Sigil != "" {
		if err := w.labelled(`,"sigil":`, plan.Sigil); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (b *_RequestBody) stamp(job, unit uint64) []byte {
	var envelope = strconv.AppendUint(append(make([]byte, 0, requestEnvelope), `{"job":`...), job, 10)
	envelope = append(envelope, ',')
	if unit != 0 {
		envelope = append(strconv.AppendUint(append(envelope, `"unit":`...), unit, 10), ',')
	}
	envelope = append(envelope, b.opens...)
	var start = requestHeadroom - len(envelope) - frameHeader
	binary.BigEndian.PutUint64(b.written[start:], uint64(len(b.written)-start-frameHeader))
	copy(b.written[start+frameHeader:], envelope)
	return b.written[start:]
}

func (w *_WireWriter) admitted(named string, value any) (_Span, error) {
	var start = len(w.written)
	if err := w.datum(value, 0); err != nil {
		switch {
		case errors.As(err, new(_PastLimit)):
			return _Span{}, err
		case errors.Is(err, errInvalidText):
			return _Span{}, fmt.Errorf("%s %w", named, err)
		}
		return _Span{}, fmt.Errorf("%s is not JSON data: %w", named, err)
	}
	if err := checkDatum(w.written[start:]); err != nil {
		return _Span{}, fmt.Errorf("%s %w", named, err)
	}
	return _Span{start: start, end: len(w.written)}, nil
}

func (w *_WireWriter) pinned(pinned _Pinned) error {
	return w.raw(`{"pinned":{"clock":` + strconv.FormatInt(pinned.Millis, 10) + `,"seed":` + strconv.FormatUint(pinned.Seed, 10) + "}")
}

func (w *_WireWriter) strings(label string, texts []string) error {
	if err := w.raw(label + "["); err != nil {
		return err
	}
	for index, text := range texts {
		if index > 0 {
			w.mark(',')
		}
		if err := w.quoted(text); err != nil {
			return err
		}
	}
	return w.raw("]")
}

func (w *_WireWriter) datum(value any, depth int) error {
	switch typed := value.(type) {
	case nil:
		return w.raw("null")
	case bool:
		return w.raw(strconv.FormatBool(typed))
	case string:
		return w.quoted(typed)
	case int:
		return w.raw(strconv.Itoa(typed))
	case int64:
		return w.raw(strconv.FormatInt(typed, 10))
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
			return w.number(typed)
		}
	case []any:
		if depth < maxDataDepth {
			return w.items(typed, depth+1)
		}
	case map[string]any:
		if depth < maxDataDepth {
			return w.members(typed, depth+1)
		}
	}
	return w.marshalled(value)
}

func (w *_WireWriter) items(items []any, depth int) error {
	if err := w.raw("["); err != nil {
		return err
	}
	for index, item := range items {
		if index > 0 {
			w.mark(',')
		}
		if err := w.datum(item, depth); err != nil {
			return err
		}
	}
	return w.raw("]")
}

func (w *_WireWriter) members(members map[string]any, depth int) error {
	if err := w.raw("{"); err != nil {
		return err
	}
	for index, key := range slices.Sorted(maps.Keys(members)) {
		if index > 0 {
			w.mark(',')
		}
		if err := w.quoted(key); err != nil {
			return err
		}
		w.mark(':')
		if err := w.datum(members[key], depth); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (w *_WireWriter) marshalled(value any) error {
	var err = json.MarshalWrite(_EncoderSink{w: w}, value)
	var syntax *jsontext.SyntacticError
	if errors.As(err, &syntax) && syntax.Err != nil && (syntax.Err.Error() == "invalid UTF-8" || strings.HasPrefix(syntax.Err.Error(), "invalid surrogate pair ")) {
		// Keep JAML's text classification while retaining the native error chain.
		return _InvalidText{cause: err}
	}
	return err
}

func (s _EncoderSink) Write(encoded []byte) (int, error) {
	if len(s.w.written)+len(encoded) > s.w.limit {
		return 0, _PastLimit{}
	}
	s.w.reserve(len(encoded))
	s.w.written = append(s.w.written, encoded...)
	return len(encoded), nil
}
