package jaml

import (
	"errors"
	"strings"

	"github.com/dop251/goja"
)

var consoleMethods = []string{Log: "log", Info: "info", Warn: "warn", Error: "error", Debug: "debug"}

func (s *_Spot) speaking(speak func(Line)) func(Level, string) {
	if speak == nil {
		return nil
	}
	return func(level Level, text string) {
		speak(Line{Path: s.where(), Level: level, Text: text})
	}
}

func (s *_Spot) where() []string {
	if piece := s.running.Load(); piece != nil {
		return piece.path.steps()
	}
	return s.selection.steps()
}

func (c *_Console) speaker() func(Line) {
	if c == nil {
		return nil
	}
	return c.say
}

func (e *_Evaluator) speaker(job uint64, asked bool) func(Line) {
	if !asked {
		return nil
	}
	return func(line Line) {
		e.replies.write(e.replies.frames.line(job, line))
	}
}

func (f _Frames) line(job uint64, line Line) []byte {
	var said = _LineOnWire(line)
	frame, _ := wireJSON(_Reply{Job: job, Line: &said})
	if len(frame) <= f.limit {
		return frame
	}
	said.Path = nil
	frame, _ = wireJSON(_Reply{Job: job, Line: &said})
	return frame
}

func (s *_Session) installConsole(speak func(Level, string)) error {
	if speak == nil {
		return nil
	}
	return s.enter(func(r _Realm) error { return r.installConsole(speak) })
}

func (r _Realm) installConsole(speak func(Level, string)) error {
	stringify, _ := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("stringify"))
	var console = r.vm.NewObject()
	for level, name := range consoleMethods {
		var method = _Native{name: name, call: func(call goja.FunctionCall) goja.Value {
			speak(Level(level), clipLine(spoken(call.Arguments, stringify)))
			return goja.Undefined()
		}}.on(r.vm)
		if _, err := r.freeze(goja.Undefined(), method); err != nil {
			return err
		}
		if err := console.DefineDataProperty(name, method, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_TRUE); err != nil {
			return err
		}
	}
	if _, err := r.freeze(goja.Undefined(), console); err != nil {
		return err
	}
	return r.vm.GlobalObject().DefineDataProperty("console", console, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_FALSE)
}

func spoken(arguments []goja.Value, stringify goja.Callable) string {
	var words = make([]string, len(arguments))
	for index, argument := range arguments {
		words[index] = word(argument, stringify)
	}
	return strings.Join(words, " ")
}

func word(argument goja.Value, stringify goja.Callable) string {
	if _, isObject := argument.(*goja.Object); !isObject {
		if text, isText := argument.Export().(string); isText {
			return text
		}
	}
	if _, isFunction := goja.AssertFunction(argument); isFunction || goja.IsUndefined(argument) {
		return argument.String()
	}
	encoded, err := stringify(goja.Undefined(), argument)
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		panic(interrupted)
	}
	if err != nil || goja.IsUndefined(encoded) {
		return argument.String()
	}
	return encoded.String()
}
