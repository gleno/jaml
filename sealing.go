package jaml

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

func (f _Frames) open(job uint64) *_WireWriter {
	return &_WireWriter{written: strconv.AppendUint([]byte(`{"job":`), job, 10), limit: f.limit}
}

func (f _Frames) built(job uint64, outline *_CodeOutline) ([]byte, error) {
	return wireJSON(_Reply{Job: job, Built: true, Code: outline})
}

func (f _Frames) usage(job uint64, used Usage) []byte {
	return fmt.Appendf(nil, `{"job":%d,"usage":{"cpu":%d,"wall":%d}}`, job, int64(used.CPU), int64(used.Wall))
}

func (f _Frames) compiled(job uint64) []byte {
	return fmt.Appendf(nil, `{"job":%d,"compiled":true}`, job)
}

func (f _Frames) missing(job uint64) []byte {
	return fmt.Appendf(nil, `{"job":%d,"missing":true}`, job)
}

func (f _Frames) answer(job uint64, result _Rendered, used Usage) []byte {
	var path = result.path.String()
	var sealed = f.open(job)
	var err = sealed.answer(result, used)
	switch {
	case errors.As(err, new(_PastLimit)):
		return f.refusal(job, path)
	case err != nil:
		return f.measuredFailure(job, path, failWith(coercionFailed, err), used)
	}
	return sealed.written
}

func (f _Frames) failure(job uint64, path string, err error) []byte {
	var sealed = f.open(job)
	if sealed.failure(located(path, err)) != nil || sealed.raw("}") != nil {
		return f.refusal(job, path)
	}
	return sealed.written
}

func (f _Frames) measuredFailure(job uint64, path string, err error, used Usage) []byte {
	var sealed = f.open(job)
	if sealed.failure(located(path, err)) != nil || sealed.usage(used) != nil || sealed.raw("}") != nil {
		return f.refusal(job, path)
	}
	return sealed.written
}

func (w *_WireWriter) usage(used Usage) error {
	return w.raw(fmt.Sprintf(`,"usage":{"cpu":%d,"wall":%d}`, int64(used.CPU), int64(used.Wall)))
}

func (f _Frames) refusal(job uint64, path string) []byte {
	var unbounded = _Frames{limit: math.MaxInt}
	return unbounded.failure(job, path, fmt.Errorf("the answer is larger than %d bytes", f.limit))
}

func (w *_WireWriter) answer(result _Rendered, used Usage) error {
	if err := w.raw(`,"data":`); err != nil {
		return err
	}
	if err := w.data(result.tree); err != nil {
		return err
	}
	if len(result.skipped) > 0 {
		if err := w.paths(result.skipped); err != nil {
			return err
		}
	}
	if err := w.usage(used); err != nil {
		return err
	}
	return w.raw("}")
}

func (w *_WireWriter) paths(paths [][]string) error {
	if err := w.raw(`,"skipped":[`); err != nil {
		return err
	}
	for index, path := range paths {
		if index > 0 {
			w.mark(',')
		}
		if err := w.steps(path); err != nil {
			return err
		}
	}
	return w.raw("]")
}

func (w *_WireWriter) steps(path []string) error {
	if path == nil {
		return w.raw("null")
	}
	w.mark('[')
	for index, step := range path {
		if index > 0 {
			w.mark(',')
		}
		if err := w.quoted(step); err != nil {
			return err
		}
	}
	return w.raw("]")
}

func (w *_WireWriter) failure(failed _ExpressionError) error {
	var described = describeFailure(failed)
	if err := w.raw(`,"failure":{"kind":`); err != nil {
		return err
	}
	if err := w.quoted(described.Kind); err != nil {
		return err
	}
	if described.At != "" {
		if err := w.labelled(`,"at":`, described.At); err != nil {
			return err
		}
	}
	if described.Label != "" {
		if err := w.labelled(`,"label":`, described.Label); err != nil {
			return err
		}
	}
	if described.Cause != "" {
		if err := w.labelled(`,"cause":`, described.Cause); err != nil {
			return err
		}
	}
	if err := w.labelled(`,"message":`, described.Message); err != nil {
		return err
	}
	var caught *Thrown
	if errors.As(failed, &caught) {
		if err := w.thrown(caught); err != nil {
			return err
		}
	}
	return w.raw("}")
}

func (w *_WireWriter) labelled(label string, text string) error {
	if err := w.raw(label); err != nil {
		return err
	}
	return w.quoted(text)
}

func (w *_WireWriter) thrown(caught *Thrown) error {
	if err := w.labelled(`,"thrown":{"message":`, caught.Message); err != nil {
		return err
	}
	if err := w.raw(`,"value":`); err != nil {
		return err
	}
	var mark = len(w.written)
	var err = w.data(caught.Value)
	switch {
	case errors.As(err, new(_PastLimit)):
		return err
	case err != nil:
		w.written = w.written[:mark]
		return w.raw("null}")
	}
	return w.raw("}")
}
