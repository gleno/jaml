package jaml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unsafe"

	"github.com/dop251/goja"
)

const pacedEvery = 256

type _Pace struct {
	ctx context.Context
	vm  *goja.Runtime
}

func (p _Pace) keep(walked int) error {
	if walked%pacedEvery != 0 || p.ctx.Err() == nil {
		return nil
	}
	p.vm.Interrupt(errInterrupted)
	return errInterrupted
}

type _Closing interface {
	settle(r _Realm, object *goja.Object) error
}

type _ValueBuild struct {
	realm   _Realm
	closing _Closing
	text    []byte
	at      int
	walked  int
}

func (r _Realm) build(datum _Datum, closing _Closing) (goja.Value, error) {
	var b = &_ValueBuild{realm: r, closing: closing, text: datum.encoded}
	value, err := b.value()
	if err != nil {
		return nil, err
	}
	if b.space(); b.at < len(b.text) {
		return nil, b.unexpected()
	}
	return value, nil
}

func (b *_ValueBuild) value() (goja.Value, error) {
	b.walked++
	if err := b.realm.pace.keep(b.walked); err != nil {
		return nil, err
	}
	switch b.space(); b.next() {
	case '{':
		return b.object()
	case '[':
		return b.array()
	case '"':
		text, err := b.string()
		if err != nil {
			return nil, err
		}
		return b.realm.vm.ToValue(text), nil
	case 't':
		return b.literal("true", b.realm.vm.ToValue(true))
	case 'f':
		return b.literal("false", b.realm.vm.ToValue(false))
	case 'n':
		return b.literal("null", goja.Null())
	}
	return b.number()
}

func (b *_ValueBuild) object() (goja.Value, error) {
	var object = b.realm.vm.NewObject()
	b.at++
	if b.space(); b.next() == '}' {
		b.at++
		return b.settled(object)
	}
	for {
		b.space()
		key, err := b.string()
		if err != nil {
			return nil, err
		}
		if err := b.expect(':'); err != nil {
			return nil, err
		}
		value, err := b.value()
		if err != nil {
			return nil, err
		}
		if err := object.DefineDataProperty(key, value, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
			return nil, err
		}
		if done, err := b.closes('}'); done || err != nil {
			if err != nil {
				return nil, err
			}
			return b.settled(object)
		}
	}
}

func (b *_ValueBuild) array() (goja.Value, error) {
	var items []any
	b.at++
	if b.space(); b.next() == ']' {
		b.at++
		return b.settled(b.realm.vm.NewArray())
	}
	for {
		item, err := b.value()
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if done, err := b.closes(']'); done || err != nil {
			if err != nil {
				return nil, err
			}
			return b.settled(b.realm.vm.NewArray(items...))
		}
	}
}

func (b *_ValueBuild) settled(object *goja.Object) (goja.Value, error) {
	if err := b.closing.settle(b.realm, object); err != nil {
		return nil, err
	}
	return object, nil
}

func (b *_ValueBuild) closes(closing byte) (bool, error) {
	switch b.space(); b.next() {
	case ',':
		b.at++
		return false, nil
	case closing:
		b.at++
		return true, nil
	}
	return false, b.unexpected()
}

func (b *_ValueBuild) string() (string, error) {
	if b.next() != '"' {
		return "", b.unexpected()
	}
	var end = textEnd(b.text, b.at+1)
	if end == len(b.text) {
		return "", b.unexpected()
	}
	var quoted = b.text[b.at : end+1]
	b.at = end + 1
	if bytes.IndexByte(quoted, '\\') < 0 {
		return string(quoted[1 : len(quoted)-1]), nil
	}
	var text string
	if err := json.Unmarshal(quoted, &text); err != nil {
		return "", fmt.Errorf("SyntaxError: %w", err)
	}
	return text, nil
}

func (b *_ValueBuild) number() (goja.Value, error) {
	var start = b.at
	for b.at < len(b.text) && isNumberByte(b.text[b.at]) {
		b.at++
	}
	if start == b.at {
		return nil, b.unexpected()
	}
	var written = b.text[start:b.at]
	number, err := strconv.ParseFloat(unsafe.String(unsafe.SliceData(written), len(written)), 64)
	if err != nil {
		return nil, fmt.Errorf("SyntaxError: the number %s is not a float64", written)
	}
	return b.realm.vm.ToValue(number), nil
}

func (b *_ValueBuild) literal(word string, value goja.Value) (goja.Value, error) {
	if len(b.text)-b.at < len(word) || string(b.text[b.at:b.at+len(word)]) != word {
		return nil, b.unexpected()
	}
	b.at += len(word)
	return value, nil
}

func (b *_ValueBuild) expect(wanted byte) error {
	if b.space(); b.next() != wanted {
		return b.unexpected()
	}
	b.at++
	return nil
}

func (b *_ValueBuild) next() byte {
	if b.at >= len(b.text) {
		return 0
	}
	return b.text[b.at]
}

func (b *_ValueBuild) space() {
	for b.at < len(b.text) && isJSONSpace(b.text[b.at]) {
		b.at++
	}
}

func (b *_ValueBuild) unexpected() error {
	if b.at >= len(b.text) {
		return fmt.Errorf("SyntaxError: unexpected end of JSON input")
	}
	return fmt.Errorf("SyntaxError: unexpected %q at byte %d of JSON input", b.text[b.at], b.at)
}

func isJSONSpace(written byte) bool {
	return written == ' ' || written == '\t' || written == '\n' || written == '\r'
}

func isNumberByte(written byte) bool {
	return written >= '0' && written <= '9' || written == '-' || written == '+' || written == '.' || written == 'e' || written == 'E'
}
