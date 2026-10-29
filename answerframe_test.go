package jaml

import (
	"encoding/json/v2"
	"errors"
	"math"
	"strings"
	"testing"
)

const answerSize = 16 << 20

func sealedAnswer(t *testing.T, f _Frames, tree any) []byte {
	t.Helper()
	return f.answer(1, _Rendered{tree: tree}, Usage{})
}

func TestAnAnswerIsWrittenOnceIntoItsFrame(t *testing.T) {
	var f = _Frames{limit: answerSize + 1<<20}
	var answer = strings.Repeat("x", answerSize)
	var frame []byte
	var spent = allocatedBy(func() { frame = sealedAnswer(t, f, answer) })
	if len(frame) < answerSize {
		t.Fatalf("the frame holds %d bytes, want the whole answer", len(frame))
	}
	if limit := answerSize * 13 / 10; spent > uint64(limit) {
		t.Fatalf("sealing a %d-byte answer allocated %d bytes (%.1fx), want under %d", answerSize, spent, float64(spent)/answerSize, limit)
	}
}

func TestAnAnswerPastTheCapIsRefusedBeforeItIsWritten(t *testing.T) {
	var f = _Frames{limit: answerSize / 4}
	var answer = strings.Repeat("x", answerSize)
	var frame []byte
	var spent = allocatedBy(func() { frame = sealedAnswer(t, f, answer) })
	if !strings.Contains(string(frame), "the answer is larger than 4194304 bytes") {
		t.Fatalf("an answer past the cap was not refused: %.200s", frame)
	}
	if limit := uint64(f.limit) / 10; spent > limit {
		t.Fatalf("refusing a %d-byte answer past a %d-byte cap allocated %d bytes, want under %d", answerSize, f.limit, spent, limit)
	}
}

func TestAManyPartAnswerPastTheCapStopsNearTheCap(t *testing.T) {
	var f = _Frames{limit: answerSize / 4}
	var part = strings.Repeat("y", 1<<10)
	var answer = make([]any, answerSize/len(part))
	for index := range answer {
		answer[index] = part
	}
	var frame []byte
	var spent = allocatedBy(func() { frame = sealedAnswer(t, f, answer) })
	if !strings.Contains(string(frame), "the answer is larger than 4194304 bytes") {
		t.Fatalf("an answer past the cap was not refused: %.200s", frame)
	}
	if limit := uint64(f.limit) * 3; spent > limit {
		t.Fatalf("refusing a %d-byte answer past a %d-byte cap allocated %d bytes, want under %d", answerSize, f.limit, spent, limit)
	}
}

func wireTexts() []string {
	var every strings.Builder
	for code := range 0x80 {
		every.WriteByte(byte(code))
	}
	return []string{"", every.String(), "\u2028\u2029", "é😀\uFFFD", "<b>&amp;</b>", "\"quoted\" \\ back", strings.Repeat("\x01", 9)}
}

func wireData() []any {
	var data = []any{nil, true, false, int64(0), int64(-7), int64(math.MaxInt64), int64(math.MinInt64)}
	for _, number := range []float64{0, math.Copysign(0, -1), 0.1 + 0.2, 1e21, 1e20, 1e-6, 1e-7, 5e-324, math.MaxFloat64, -2.5, 123456789012} {
		data = append(data, number)
	}
	for _, text := range wireTexts() {
		data = append(data, text, map[string]any{text: text})
	}
	return append(data,
		[]any{}, []any(nil), map[string]any{}, map[string]any(nil),
		map[string]any{"b": []any{int64(1), "x", nil}, "a": map[string]any{"z": false, "y": 1.5}},
		&_Mapping{keys: []string{"z", "a"}, values: map[string]any{"z": int64(1), "a": []any{"q"}}},
	)
}

var wireWritten = []string{
	`null`,
	`true`,
	`false`,
	`0`,
	`-7`,
	`9223372036854775807`,
	`-9223372036854775808`,
	`0`,
	`-0`,
	`0.3`,
	`1e+21`,
	`100000000000000000000`,
	`0.000001`,
	`1e-7`,
	`5e-324`,
	`1.7976931348623157e+308`,
	`-2.5`,
	`123456789012`,
	`""`,
	`{"":""}`,
	"\"\\u0000\\u0001\\u0002\\u0003\\u0004\\u0005\\u0006\\u0007\\b\\t\\n\\u000b\\f\\r\\u000e\\u000f\\u0010\\u0011\\u0012\\u0013\\u0014\\u0015\\u0016\\u0017\\u0018\\u0019\\u001a\\u001b\\u001c\\u001d\\u001e\\u001f !\\\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\x7f\"",
	"{\"\\u0000\\u0001\\u0002\\u0003\\u0004\\u0005\\u0006\\u0007\\b\\t\\n\\u000b\\f\\r\\u000e\\u000f\\u0010\\u0011\\u0012\\u0013\\u0014\\u0015\\u0016\\u0017\\u0018\\u0019\\u001a\\u001b\\u001c\\u001d\\u001e\\u001f !\\\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\x7f\":\"\\u0000\\u0001\\u0002\\u0003\\u0004\\u0005\\u0006\\u0007\\b\\t\\n\\u000b\\f\\r\\u000e\\u000f\\u0010\\u0011\\u0012\\u0013\\u0014\\u0015\\u0016\\u0017\\u0018\\u0019\\u001a\\u001b\\u001c\\u001d\\u001e\\u001f !\\\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\x7f\"}",
	"\"\u2028\u2029\"",
	"{\"\u2028\u2029\":\"\u2028\u2029\"}",
	`"é😀�"`,
	`{"é😀�":"é😀�"}`,
	`"<b>&amp;</b>"`,
	`{"<b>&amp;</b>":"<b>&amp;</b>"}`,
	`"\"quoted\" \\ back"`,
	`{"\"quoted\" \\ back":"\"quoted\" \\ back"}`,
	`"\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001"`,
	`{"\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001":"\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001\u0001"}`,
	`[]`,
	`[]`,
	`{}`,
	`{}`,
	`{"a":{"y":1.5,"z":false},"b":[1,"x",null]}`,
	`{"z":1,"a":["q"]}`,
}

func TestAnAnswerFrameIsByteForByteTheWireEncoding(t *testing.T) {
	var f = _Frames{limit: 1 << 20}
	var data = wireData()
	expectEqual(t, len(data), len(wireWritten))
	for index, value := range data {
		var want = `{"job":42,"at":"$","data":` + wireWritten[index] + `,"skipped":[["a","` + "\u2028" + `"],[]],"usage":{"cpu":3,"wall":7}}`
		expectEqual(t, string(f.answer(42, _Rendered{tree: value, skipped: [][]string{{"a", "\u2028"}, {}}}, Usage{CPU: 3, Wall: 7})), want)
	}
}

func TestAFailureFrameIsByteForByteTheWireEncoding(t *testing.T) {
	var f = _Frames{limit: 1 << 20}
	var data = wireData()
	expectEqual(t, len(data), len(wireWritten))
	var message = "threw \u2028 something"
	for index, value := range data {
		var thrown = &Thrown{Message: "threw \u2028 something", Value: value}
		var want = `{"job":7,"failure":{"kind":"EvaluationFailed","at":"$.v","message":"` + message + `","thrown":{"message":"` + message + `","value":` + wireWritten[index] + `}}}`
		expectEqual(t, string(f.failure(7, "$.v", failWith(evaluationFailed, thrown).at("$.v"))), want)
	}
}

func TestTheSmallFramesAreTheirWireEncoding(t *testing.T) {
	var f = _Frames{limit: 1 << 20}
	var outline = &_CodeOutline{steps: map[string]*_CodeOutline{"a": {}, "b": {steps: map[string]*_CodeOutline{"0": {}}}}}
	var built, err = f.built(3, nil)
	if err != nil {
		t.Fatal(err)
	}
	outlined, err := f.built(6, outline)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, string(outlined), `{"job":6,"built":true,"code":{"a":true,"b":{"0":true}}}`)
	expectEqual(t, string(built), `{"job":3,"built":true}`)
	expectEqual(t, string(f.missing(4)), `{"job":4,"missing":true}`)
	expectEqual(t, string(f.failure(5, "$.a", errors.New("plain"))), `{"job":5,"failure":{"kind":"EvaluationFailed","at":"$.a","message":"plain"}}`)
}

func TestAFrameAtExactlyTheCapIsSentAndOneByteMoreIsRefused(t *testing.T) {
	var f = _Frames{limit: 1 << 10}
	var overhead = len(`{"job":1,"at":"$","data":"` + "\u2028" + `","usage":{"cpu":0,"wall":0}}`)
	var fitting = strings.Repeat("x", f.limit-overhead) + "\u2028"
	expectEqual(t, len(f.answer(1, _Rendered{tree: fitting}, Usage{})), f.limit)
	var refused = string(f.answer(1, _Rendered{tree: fitting + "x"}, Usage{}))
	if !strings.Contains(refused, "the answer is larger than 1024 bytes") {
		t.Fatalf("a frame one byte past the cap was sent: %.80s", refused)
	}
}

func TestAThrownValuePastTheCapIsRefusedAndOneUnderIsCarried(t *testing.T) {
	var f = _Frames{limit: 1 << 10}
	for _, size := range []int{850, 1100} {
		var err = failWith(evaluationFailed, &Thrown{Message: "threw a string", Value: strings.Repeat("x", size)})
		var sent _Reply
		if decodeErr := json.Unmarshal(f.failure(1, "$", err), &sent); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var rebuilt = sent.Failure.rebuild()
		var caught *Thrown
		switch carried := errors.As(rebuilt, &caught); {
		case size < f.limit && (!carried || caught.Value != strings.Repeat("x", size)):
			t.Fatalf("a %d-byte thrown value under the cap was not carried: %v", size, rebuilt)
		case size > f.limit && !strings.Contains(rebuilt.Error(), "the answer is larger than 1024 bytes"):
			t.Fatalf("a %d-byte thrown value past the cap was not refused: %v", size, rebuilt)
		}
	}
}

func TestFrameEncodingRejectsInvalidUTF8WithoutRecursing(t *testing.T) {
	var frames = _Frames{limit: 1024}
	for _, data := range []any{"bad\xff", map[string]any{"bad\xff": 1}} {
		var reply _Reply
		if err := json.Unmarshal(frames.answer(1, _Rendered{tree: data}, Usage{}), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Failure == nil {
			t.Fatal("invalid text was sent as data")
		}
	}
	var reply _Reply
	if err := json.Unmarshal(frames.failure(1, "$[\xff]", errors.New("bad\xff")), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Failure == nil {
		t.Fatal("invalid failure was not refused")
	}
}

func TestNativeReplyEncodingKeepsEmptyJSONResults(t *testing.T) {
	for _, data := range []string{"null", "{}", "[]", `""`, "false", "0"} {
		encoded, err := wireJSON(_Reply{Job: 1, Data: _Borrowed(data), At: "$"})
		if err != nil {
			t.Fatal(err)
		}
		var reply _Reply
		if err := json.Unmarshal(encoded, &reply); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, string(reply.Data), data)
		expectEqual(t, reply.outcomes(), 1)
	}
	encoded, err := wireJSON(_Reply{Job: 1, Missing: true})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, string(encoded), `{"job":1,"missing":true}`)
}

func TestAnswerLocationsRoundTripAndCountTowardsTheCap(t *testing.T) {
	for _, path := range []*_DataPath{nil, (*_DataPath)(nil).atKey("0").atIndex(0).atKey("0"), (*_DataPath)(nil).atKey("\n\"\\é\u2028")} {
		var frames = _Frames{limit: 1024}
		var want = path.String()
		reference, err := wireJSON(_Reply{Job: 1, Data: _Borrowed(`""`), At: want, Usage: &_UsageOnWire{}})
		if err != nil {
			t.Fatal(err)
		}
		var fitting = strings.Repeat("x", frames.limit-len(reference))
		var frame = frames.answer(1, _Rendered{tree: fitting, path: path}, Usage{})
		expectEqual(t, len(frame), frames.limit)
		var reply _Reply
		if err := json.Unmarshal(frame, &reply); err != nil {
			t.Fatal(err)
		}
		if err := reply.fits(opServe); err != nil {
			t.Fatal(err)
		}
		expectEqual(t, reply.At, want)
		expectEqual(t, servedFrom(reply).(_Answered).at, want)
		if err := json.Unmarshal(frames.answer(1, _Rendered{tree: fitting + "x", path: path}, Usage{}), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Failure == nil {
			t.Fatal("the selected location was excluded from the frame cap")
		}
		expectEqual(t, reply.Failure.At, want)
	}
}

func TestOnlyDataRepliesCarryAResolvedLocation(t *testing.T) {
	for _, reply := range []_Reply{
		{Job: 1, Data: _Borrowed("1")},
		{Job: 1, Built: true, At: "$"},
		{Job: 1, Missing: true, At: "$"},
		{Job: 1, Failure: &_Failure{Kind: "EvaluationFailed", At: "$", Message: "failed"}, At: "$"},
		{Job: 1, Compiled: true, At: "$"},
		{Job: 1, Line: &_LineOnWire{}, At: "$"},
		{Job: 1, Usage: &_UsageOnWire{}, At: "$"},
	} {
		if reply.fits(opServe) == nil {
			t.Fatalf("invalid resolved location accepted: %+v", reply)
		}
		if new(_Switchboard).route(reply) == nil {
			t.Fatalf("invalid resolved location routed: %+v", reply)
		}
	}
}
