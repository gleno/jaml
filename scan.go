package jaml

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type _CodeShape int

const (
	nestingShape _CodeShape = iota
	arrowShape
	statementShape
	prefixShape
	rightShape
	binaryShape
	postfixShape
	shapeCount
)

type _CodeShapeBound struct {
	limit int
	what  string
}

var shapeBounds = [shapeCount]_CodeShapeBound{
	nestingShape:   {512, "brackets, braces, parentheses, template substitutions and classes nest more than %d deep"},
	arrowShape:     {512, "arrow functions nest more than %d deep"},
	statementShape: {512, "statements (if, else, loops and labels) nest more than %d deep"},
	prefixShape:    {512, "more than %d prefix operators apply in a row"},
	rightShape:     {512, "more than %d assignments, conditionals and exponentiations chain"},
	binaryShape:    {4096, "more than %d binary operators chain"},
	postfixShape:   {2048, "more than %d member accesses, calls and tagged templates chain"},
}

type _Counts [shapeCount]int

func (c _Counts) plus(other _Counts) _Counts {
	for shape := range c {
		c[shape] += other[shape]
	}
	return c
}

type _FrameKind int

const (
	groupFrame _FrameKind = iota
	controlFrame
	indexFrame
	objectFrame
	classFrame
	blockFrame
	bodyFrame
	templateFrame
	substitutionFrame
)

func (k _FrameKind) holdsStatements() bool {
	return k == blockFrame || k == bodyFrame
}

func (k _FrameKind) closer() byte {
	switch k {
	case groupFrame, controlFrame:
		return ')'
	case indexFrame:
		return ']'
	case templateFrame:
		return '`'
	}
	return '}'
}

type _Settling int

const (
	unsettled _Settling = iota
	settledByLine
	settledByStatement
)

type _Frame struct {
	kind      _FrameKind
	base      _Counts
	local     _Counts
	questions int
	classes   int
	settling  _Settling
}

func (f *_Frame) depth(shape _CodeShape) int {
	return f.base[shape] + f.local[shape]
}

func (f *_Frame) endExpression() {
	f.local[arrowShape], f.local[rightShape], f.local[binaryShape], f.local[prefixShape], f.local[postfixShape] = 0, 0, 0, 0, 0
	f.questions = 0
}

func (f *_Frame) beginOperand() {
	f.local[binaryShape], f.local[prefixShape], f.local[postfixShape] = 0, 0, 0
}

type _Token int

const (
	startToken _Token = iota
	operandToken
	operatorToken
	arrowToken
	controlCloseToken
	groupCloseToken
	blockCloseToken
	keywordToken
)

type _CodeScan struct {
	text     string
	closes   string
	at       int
	mark     int
	frames   []_Frame
	last     _Token
	keyword  string
	property bool
	control  bool
}

type _CodeLimit struct {
	offset int
	shape  _CodeShape
}

func (l _CodeLimit) Error() string {
	return fmt.Sprintf(shapeBounds[l.shape].what, shapeBounds[l.shape].limit)
}

type _ScanStop struct {
	offset  int
	message string
}

func (s _ScanStop) Error() string {
	return s.message
}

type _Unclosed struct{}

func (_Unclosed) Error() string {
	return "unclosed"
}

func codeEnd(after string, opened _Delimiter) (int, error) {
	end, err := newCodeScan(after, opened.closes, groupFrame).run()
	if _, unclosed := err.(_Unclosed); unclosed {
		return 0, fmt.Errorf("unclosed %s", opened.opens)
	}
	return end, placeScanError(after, err)
}

func refuseDeepExpression(source string) error {
	return refuseDeepCode(source, groupFrame)
}

func refuseDeepScript(source string) error {
	return refuseDeepCode(source, blockFrame)
}

func refuseDeepCode(source string, outer _FrameKind) error {
	_, err := newCodeScan(source, "", outer).run()
	if _, unclosed := err.(_Unclosed); unclosed {
		return syntaxErrorAt(source, len(source), "Unexpected end of input")
	}
	return placeScanError(source, err)
}

func placeScanError(source string, err error) error {
	switch failure := err.(type) {
	case _ScanStop:
		return syntaxErrorAt(source, failure.offset, failure.message)
	case _CodeLimit:
		var before = source[:failure.offset]
		var line = strings.Count(before, "\n") + 1
		var column = utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:]) + 1
		return fmt.Errorf("code too deep at line %d, column %d: %w", line, column, failure)
	}
	return err
}

func newCodeScan(text, closes string, outer _FrameKind) *_CodeScan {
	return &_CodeScan{text: text, closes: closes, frames: []_Frame{{kind: outer}}}
}

func (s *_CodeScan) top() *_Frame {
	return &s.frames[len(s.frames)-1]
}

func (s *_CodeScan) expectsOperand() bool {
	switch s.last {
	case operandToken, groupCloseToken:
		return false
	case keywordToken:
		return s.keyword != "class" && s.keyword != "function"
	}
	return true
}

func (s *_CodeScan) run() (int, error) {
	for {
		if s.top().kind == templateFrame {
			if err := s.template(); err != nil {
				return 0, err
			}
			continue
		}
		var before = s.at
		if err := s.skipSpace(); err != nil {
			return 0, err
		}
		s.settleAtLineEnd(s.text[before:s.at])
		if s.at >= len(s.text) {
			if s.closes == "" && len(s.frames) == 1 {
				return len(s.text), nil
			}
			return 0, _Unclosed{}
		}
		s.mark = s.at
		s.settle()
		end, done, err := s.token()
		if err != nil || done {
			return end, err
		}
	}
}

func (s *_CodeScan) skipSpace() error {
	for s.at < len(s.text) {
		var c = s.text[s.at]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f':
			s.at++
		case c == '/' && s.byteAt(s.at+1) == '/':
			s.skipLineComment()
		case c == '/' && s.byteAt(s.at+1) == '*':
			var end = strings.Index(s.text[s.at+2:], "*/")
			if end < 0 {
				s.at = len(s.text)
				return _Unclosed{}
			}
			s.at += 2 + end + 2
		case c >= utf8.RuneSelf:
			r, size := utf8.DecodeRuneInString(s.text[s.at:])
			if !isSpace(r) {
				return nil
			}
			s.at += size
		default:
			return nil
		}
	}
	return nil
}

func isSpace(r rune) bool {
	return unicode.IsSpace(r) || r == '\ufeff'
}

func (s *_CodeScan) skipLineComment() {
	var end = strings.IndexAny(s.text[s.at:], "\n\r  ")
	if end < 0 {
		s.at = len(s.text)
		return
	}
	s.at += end
}

func (s *_CodeScan) settleAtLineEnd(skipped string) {
	var frame = s.top()
	if frame.kind.holdsStatements() && frame.settling == unsettled && !s.expectsOperand() && strings.ContainsAny(skipped, "\n\r  ") {
		frame.settling = settledByLine
	}
}

func (s *_CodeScan) settle() {
	var frame = s.top()
	var settling = frame.settling
	frame.settling = unsettled
	switch {
	case settling == unsettled || s.startsWord("else"):
	case settling == settledByStatement:
		frame.local[statementShape] = 0
	case s.startsStatement():
		frame.local[statementShape] = 0
		frame.endExpression()
	}
}

func (s *_CodeScan) startsWord(word string) bool {
	return strings.HasPrefix(s.text[s.at:], word) && !isWordByte(s.byteAt(s.at+len(word)))
}

func (s *_CodeScan) startsStatement() bool {
	var c = s.text[s.at]
	switch {
	case s.startsWord("in") || s.startsWord("instanceof"):
		return false
	case c == '"' || c == '\'' || c == '{' || c == '#':
		return true
	}
	return isWordByte(c)
}

func (s *_CodeScan) token() (int, bool, error) {
	var c = s.text[s.at]
	switch {
	case c == '"' || c == '\'':
		return 0, false, s.quoted(c)
	case c == '`':
		return 0, false, s.openTemplate()
	case isDigit(c) || c == '.' && isDigit(s.byteAt(s.at+1)):
		s.number()
		s.fresh()
		return 0, false, nil
	case c == '#' || isWordByte(c):
		return 0, false, s.word()
	case c == '(' || c == '[' || c == '{':
		return 0, false, s.open(c)
	case c == ')' || c == ']':
		return 0, false, s.close(c)
	case c == '}':
		return s.closeBrace()
	case c == '/' && s.expectsOperand():
		return 0, false, s.regex()
	}
	return 0, false, s.punctuator()
}

func (s *_CodeScan) byteAt(offset int) byte {
	if offset >= len(s.text) {
		return 0
	}
	return s.text[offset]
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isWordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || isDigit(c) || c == '_' || c == '$' || c == '\\' || c >= utf8.RuneSelf
}

func (s *_CodeScan) count(shape _CodeShape) error {
	var frame = s.top()
	frame.local[shape]++
	if frame.depth(shape) > shapeBounds[shape].limit {
		return _CodeLimit{offset: s.mark, shape: shape}
	}
	return nil
}

func (s *_CodeScan) operand() {
	s.top().local[prefixShape] = 0
	s.last, s.property, s.control = operandToken, false, false
}

func (s *_CodeScan) fresh() {
	s.top().local[postfixShape] = 0
	s.operand()
}

func (s *_CodeScan) quoted(quote byte) error {
	for s.at++; s.at < len(s.text); s.at++ {
		switch s.text[s.at] {
		case quote:
			s.at++
			s.fresh()
			return nil
		case '\\':
			s.at++
			if strings.HasPrefix(s.text[s.at:], "\r\n") {
				s.at++
			}
		case '\n', '\r':
			return _ScanStop{offset: s.mark, message: "Unexpected token ILLEGAL"}
		}
	}
	return _ScanStop{offset: s.mark, message: "Unexpected token ILLEGAL"}
}

func (s *_CodeScan) regex() error {
	var inClass bool
	for s.at++; s.at < len(s.text); s.at++ {
		switch s.text[s.at] {
		case '\\':
			s.at++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			s.at++
			for s.at < len(s.text) && isWordByte(s.text[s.at]) {
				s.at++
			}
			s.fresh()
			return nil
		case '\n', '\r':
			return _ScanStop{offset: s.mark, message: "Invalid regular expression: missing /"}
		}
	}
	return _ScanStop{offset: s.mark, message: "Invalid regular expression: missing /"}
}

func (s *_CodeScan) number() {
	var radix = s.text[s.at] == '0' && strings.IndexByte("xXoObB", s.byteAt(s.at+1)) >= 0
	if radix {
		s.at += 2
	}
	var dotted, exponent bool
	for s.at < len(s.text) {
		var c = s.text[s.at]
		switch {
		case c == '.' && !dotted && !exponent && !radix:
			dotted = true
		case (c == 'e' || c == 'E') && !exponent && !radix:
			exponent = true
			if next := s.byteAt(s.at + 1); next == '+' || next == '-' {
				s.at++
			}
		case isWordByte(c):
		default:
			return
		}
		s.at++
	}
}

func (s *_CodeScan) word() error {
	var start = s.at
	if s.text[s.at] == '#' {
		s.at++
	}
	for s.at < len(s.text) {
		var c = s.text[s.at]
		switch {
		case c == '\\' && strings.HasPrefix(s.text[s.at:], "\\u{"):
			var end = strings.IndexByte(s.text[s.at:], '}')
			if end < 0 {
				return _ScanStop{offset: s.at, message: "Unexpected token ILLEGAL"}
			}
			s.at += end + 1
		case c >= utf8.RuneSelf:
			r, size := utf8.DecodeRuneInString(s.text[s.at:])
			if isSpace(r) {
				return s.named(s.text[start:s.at])
			}
			s.at += size
		case isWordByte(c):
			s.at++
		default:
			return s.named(s.text[start:s.at])
		}
	}
	return s.named(s.text[start:s.at])
}

func (s *_CodeScan) named(name string) error {
	if s.property {
		s.operand()
		return nil
	}
	var frame = s.top()
	var statements = frame.kind.holdsStatements()
	var control = s.control && name == "await"
	var err error
	switch name {
	case "this", "super", "null", "true", "false":
		s.fresh()
		return nil
	case "if", "while", "for", "with":
		if statements {
			err = s.count(statementShape)
		}
		control = true
	case "do":
		if statements {
			err = s.count(statementShape)
		}
	case "case", "default":
		if statements {
			frame.local[statementShape] = 0
		}
	case "typeof", "void", "delete", "new", "await", "yield", "extends":
		err = s.count(prefixShape)
	case "in", "instanceof":
		err = s.binary()
	case "class":
		frame.classes++
		err = s.count(nestingShape)
	case "return", "throw", "try", "finally", "function", "else":
		frame.endExpression()
	default:
		s.fresh()
		return nil
	}
	s.last, s.keyword, s.control = keywordToken, name, control
	return err
}

func (s *_CodeScan) binary() error {
	var frame = s.top()
	frame.local[prefixShape], frame.local[postfixShape] = 0, 0
	return s.count(binaryShape)
}

func (s *_CodeScan) openTemplate() error {
	if s.expectsOperand() {
		s.top().local[postfixShape] = 0
	} else if err := s.count(postfixShape); err != nil {
		return err
	}
	s.at++
	s.push(templateFrame)
	return nil
}

func (s *_CodeScan) template() error {
	for s.at < len(s.text) {
		switch {
		case s.text[s.at] == '\\':
			s.at += 2
		case s.text[s.at] == '`':
			s.at++
			s.frames = s.frames[:len(s.frames)-1]
			s.operand()
			return nil
		case strings.HasPrefix(s.text[s.at:], "${"):
			s.mark = s.at
			s.at += 2
			s.last = operatorToken
			s.push(substitutionFrame)
			return s.nest()
		default:
			s.at++
		}
	}
	return _Unclosed{}
}

func (s *_CodeScan) push(kind _FrameKind) {
	var parent = s.top()
	s.frames = append(s.frames, _Frame{kind: kind, base: parent.base.plus(parent.local)})
}

func (s *_CodeScan) nest() error {
	var frame = s.top()
	frame.base[nestingShape]++
	if frame.base[nestingShape] > shapeBounds[nestingShape].limit {
		return _CodeLimit{offset: s.mark, shape: nestingShape}
	}
	return nil
}

func (s *_CodeScan) open(c byte) error {
	var kind = s.openedKind(c)
	var parent = s.top()
	switch {
	case kind == objectFrame || kind == classFrame || kind.holdsStatements():
	case !s.expectsOperand() && s.last != keywordToken:
		if err := s.count(postfixShape); err != nil {
			return err
		}
	default:
		parent.local[postfixShape] = 0
	}
	s.at++
	s.last, s.control, s.property = operatorToken, false, false
	s.push(kind)
	if kind.holdsStatements() {
		s.last = startToken
	}
	if kind == classFrame {
		parent = &s.frames[len(s.frames)-2]
		parent.classes--
		parent.local[nestingShape]--
		return nil
	}
	return s.nest()
}

func (s *_CodeScan) openedKind(c byte) _FrameKind {
	var frame = s.top()
	switch {
	case c == '(' && s.control:
		return controlFrame
	case c == '(':
		return groupFrame
	case c == '[':
		return indexFrame
	case s.last == arrowToken:
		return bodyFrame
	case frame.classes > 0 && !s.expectsOperand():
		return classFrame
	case s.last == controlCloseToken:
		return blockFrame
	case s.last == groupCloseToken:
		return bodyFrame
	case s.last == keywordToken && (s.keyword == "else" || s.keyword == "do" || s.keyword == "try" || s.keyword == "finally"):
		return blockFrame
	case frame.kind.holdsStatements() && (s.last == startToken || s.last == blockCloseToken):
		return blockFrame
	}
	return objectFrame
}

func (s *_CodeScan) close(c byte) error {
	var frame = s.top()
	if len(s.frames) == 1 || frame.kind.closer() != c {
		return _ScanStop{offset: s.at, message: "Unexpected token " + string(c)}
	}
	s.at++
	s.frames = s.frames[:len(s.frames)-1]
	s.top().local[prefixShape] = 0
	s.last, s.property, s.control = groupCloseToken, false, false
	if frame.kind == controlFrame {
		s.last = controlCloseToken
	}
	return nil
}

func (s *_CodeScan) closeBrace() (int, bool, error) {
	var frame = s.top()
	var rest = s.text[s.at:]
	switch {
	case len(s.frames) > 1 && frame.kind.closer() == '}':
	case s.closes != "" && strings.HasPrefix(rest, s.closes):
		return s.at, true, nil
	case len(s.frames) == 1 && s.closes != "" && strings.HasPrefix(s.closes, rest):
		return 0, false, _Unclosed{}
	default:
		return 0, false, _ScanStop{offset: s.at, message: "Unexpected token }"}
	}
	s.at++
	s.frames = s.frames[:len(s.frames)-1]
	var parent = s.top()
	s.property, s.control = false, false
	switch frame.kind {
	case substitutionFrame:
	case blockFrame:
		s.last = blockCloseToken
		if parent.kind.holdsStatements() {
			parent.endExpression()
			parent.settling = settledByStatement
		}
	default:
		parent.local[prefixShape] = 0
		s.last = groupCloseToken
	}
	return 0, false, nil
}

var punctuators = []string{
	">>>=", "...", "===", "!==", "**=", "<<=", ">>=", ">>>", "&&=", "||=", "??=",
	"=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--", "**", "<<", ">>",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
}

var assignments = map[string]bool{"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "**=": true, "<<=": true, ">>=": true, ">>>=": true, "&=": true, "|=": true, "^=": true, "&&=": true, "||=": true, "??=": true}

func (s *_CodeScan) punctuator() error {
	var rest = s.text[s.at:]
	var written = rest[:1]
	for _, candidate := range punctuators {
		if strings.HasPrefix(rest, candidate) && !(candidate == "?." && isDigit(s.byteAt(s.at+2))) {
			written = candidate
			break
		}
	}
	var expected = s.expectsOperand()
	var frame = s.top()
	s.at += len(written)
	s.last, s.property, s.control = operatorToken, false, false
	switch {
	case written == "=>":
		frame.beginOperand()
		s.last = arrowToken
		return s.count(arrowShape)
	case assignments[written] || written == "**":
		frame.beginOperand()
		return s.count(rightShape)
	case written == "?":
		frame.beginOperand()
		frame.questions++
		return s.count(rightShape)
	case written == ":" && frame.questions > 0:
		frame.beginOperand()
		frame.questions--
	case written == ":" && frame.kind.holdsStatements():
		frame.beginOperand()
		s.last = startToken
		return s.count(statementShape)
	case written == ":":
		frame.beginOperand()
	case written == ",":
		frame.endExpression()
	case written == ";":
		frame.endExpression()
		if frame.kind.holdsStatements() {
			frame.settling = settledByStatement
			s.last = startToken
		}
	case written == "." || written == "?.":
		s.property = true
		return s.count(postfixShape)
	case (written == "++" || written == "--") && !expected:
		s.last = operandToken
	case written == "++" || written == "--" || written == "!" || written == "~" || written == "...":
		return s.count(prefixShape)
	case (written == "+" || written == "-") && expected:
		return s.count(prefixShape)
	case strings.IndexByte("=!<>+-*/%&|^?", written[0]) >= 0:
		return s.binary()
	default:
		return _ScanStop{offset: s.mark, message: "Unexpected token ILLEGAL"}
	}
	return nil
}
