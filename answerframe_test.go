package jaml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

const answerSize = 16 << 20

func sealedAnswer(t *testing.T, f _Frames, tree any) []byte {
	t.Helper()
	return f.answer(1, _Rendered{tree: tree})
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
	return []string{"", every.String(), "\u2028\u2029", "é😀\uFFFD", "<b>&amp;</b>", "bad \xff\xfe utf-8", "\"quoted\" \\ back", strings.Repeat("\x01", 9)}
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

func asWire(node any) any {
	switch typed := node.(type) {
	case *_Mapping:
		var ordered = make([]json.RawMessage, len(typed.keys))
		for index, key := range typed.keys {
			var field = map[string]any{key: asWire(typed.values[key])}
			encoded, _ := wireJSON(field)
			ordered[index] = encoded[1 : len(encoded)-1]
		}
		var joined = "{" + string(json.RawMessage(strings.Join(rawStrings(ordered), ","))) + "}"
		return json.RawMessage(joined)
	case []any:
		if typed == nil {
			return typed
		}
		var converted = make([]any, len(typed))
		for index, item := range typed {
			converted[index] = asWire(item)
		}
		return converted
	}
	return node
}

func rawStrings(raws []json.RawMessage) []string {
	var texts []string
	for _, raw := range raws {
		texts = append(texts, string(raw))
	}
	return texts
}

func TestAnAnswerFrameIsByteForByteWhatEncodingJSONWrote(t *testing.T) {
	var f = _Frames{limit: 1 << 20}
	for _, value := range wireData() {
		data, err := wireJSON(asWire(value))
		if err != nil {
			t.Fatalf("wireJSON(%#v): %v", value, err)
		}
		want, err := wireJSON(_Reply{Job: 42, Data: data, Skipped: [][]string{{"a", "\u2028"}, {}}})
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, string(f.answer(42, _Rendered{tree: value, skipped: [][]string{{"a", "\u2028"}, {}}})), string(want))
	}
}

func TestAFailureFrameIsByteForByteWhatEncodingJSONWrote(t *testing.T) {
	var f = _Frames{limit: 1 << 20}
	for _, value := range wireData() {
		data, err := wireJSON(asWire(value))
		if err != nil {
			t.Fatal(err)
		}
		var thrown = &Thrown{Message: "threw \u2028 something", Value: value}
		err = fail(EvaluationFailed, "%w", thrown).within("$.v")
		var described = describeFailure(err)
		described.Thrown = &_ThrownOnWire{Message: thrown.Message, Value: data}
		want, marshalErr := wireJSON(_Reply{Job: 7, Failure: &described})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		expectEqual(t, string(f.failure(7, "$.v", err)), string(want))
	}
	var stopped = located("$.v", fmt.Errorf("cancelled: %w", context.Canceled))
	var described = describeFailure(stopped)
	want, err := wireJSON(_Reply{Job: 8, Failure: &described})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, described.Stopped, true)
	expectEqual(t, string(f.failure(8, "$.v", stopped)), string(want))
}

func TestTheSmallFramesAreWhatEncodingJSONWrote(t *testing.T) {
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
	for _, sent := range []struct {
		sealed []byte
		reply  _Reply
	}{
		{built, _Reply{Job: 3, Built: true}},
		{f.missing(4), _Reply{Job: 4, Missing: true}},
		{f.failure(5, "", errors.New("plain")), _Reply{Job: 5, Failure: &_Failure{Kind: "EvaluationFailed", Message: "plain"}}},
	} {
		want, err := wireJSON(sent.reply)
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, string(sent.sealed), string(want))
	}
}

func TestAFrameAtExactlyTheCapIsSentAndOneByteMoreIsRefused(t *testing.T) {
	var f = _Frames{limit: 1 << 10}
	var overhead = len(`{"job":1,"data":"\u2028"}`)
	var fitting = strings.Repeat("x", f.limit-overhead) + "\u2028"
	expectEqual(t, len(f.answer(1, _Rendered{tree: fitting})), f.limit)
	var refused = string(f.answer(1, _Rendered{tree: fitting + "x"}))
	if !strings.Contains(refused, "the answer is larger than 1024 bytes") {
		t.Fatalf("a frame one byte past the cap was sent: %.80s", refused)
	}
}

func TestAThrownValuePastTheCapIsRefusedAndOneUnderIsCarried(t *testing.T) {
	var f = _Frames{limit: 1 << 10}
	for _, size := range []int{900, 1100} {
		var err = fail(EvaluationFailed, "%w", &Thrown{Message: "threw a string", Value: strings.Repeat("x", size)})
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
