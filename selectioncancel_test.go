package jaml

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"testing"
	"testing/synctest"
	"time"
)

// Hold a successful render until the real budget watcher has dropped its job.
type _FinishingAfterBudget struct {
	_Servable
	jobDone <-chan struct{}
}

func (u _FinishingAfterBudget) prepare(request _Work, host _Host) (_Prepared, error) {
	prepared, err := u._Servable.prepare(request, host)
	if err != nil {
		return prepared, err
	}
	run := prepared.run
	prepared.run = func(ctx context.Context, speak func(Line)) (_Rendered, error) {
		result, err := run(ctx, speak)
		if err == nil {
			<-ctx.Done()
			<-u.jobDone
		}
		return result, err
	}
	return prepared, nil
}

func TestACompletedRenderDroppedByItsBudgetKeepsTheSelectedLocation(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
		path               []string
	}{
		{"mapping", `{"0":1}`, `$["0"]`, []string{"0"}},
		{"array", `[1]`, `$[0]`, []string{"0"}},
		{"mixed", `{"0":[{"0":1}]}`, `$["0"][0]["0"]`, []string{"0", "0", "0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				unit, err := (_Blueprint{Written: test.source, Null: Loose, Undefined: Strict}).build(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				jobs, stuck := watchedTable()
				ctx, err := jobs.begin(1)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				var evaluator = &_Evaluator{
					host: _Host{scripts: newScriptCache(1 << 20)}, buildTimeout: time.Hour,
					watch: &_HeapWatch{}, jobs: jobs,
					replies: &_ReplyWriter{out: &out, frames: _Frames{limit: 1 << 20}},
				}
				evaluator.serve(ctx, 1, _FinishingAfterBudget{_Servable: unit, jobDone: ctx.Done()}, _Work{Path: test.path, WallLimit: _WallLimit(time.Second)})
				frame, err := evaluator.replies.frames.replies(&out, &_Quiet{}).next()
				if err != nil {
					t.Fatal(err)
				}
				var reply _Reply
				if err := json.Unmarshal(frame, &reply); err != nil {
					t.Fatal(err)
				}
				if reply.Failure == nil {
					t.Fatalf("a dropped job returned a successful answer: %s", frame)
				}
				expectError(t, reply.Failure.rebuild(), WallLimit)
				expectEqual(t, reply.Failure.At, test.want)
				expectResident(t, jobs, 0)
				if len(stuck.noted()) != 0 {
					t.Fatal("the completed render exhausted its cancellation grace")
				}
			})
		})
	}
}
