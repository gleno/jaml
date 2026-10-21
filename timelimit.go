package jaml

import (
	"context"
	"fmt"
	goruntime "runtime"
	"time"
)

const cpuPollFloor = time.Millisecond

type _TimeLimitKey struct{}

type _TimeLimit time.Duration

type _MeterKey struct{}

type Usage struct {
	CPU  time.Duration
	Wall time.Duration
}

func WithTimeLimit(ctx context.Context, limit time.Duration) context.Context {
	if earlier, limited := ctx.Value(_TimeLimitKey{}).(_TimeLimit); limited && time.Duration(earlier) <= limit {
		return ctx
	}
	return context.WithValue(ctx, _TimeLimitKey{}, _TimeLimit(limit))
}

func timeLimitOf(ctx context.Context) (_TimeLimit, error) {
	limit, limited := ctx.Value(_TimeLimitKey{}).(_TimeLimit)
	if limited && limit <= 0 {
		return 0, fail(invalidInput, "a time limit must be positive, got %s", time.Duration(limit)).at(rootPlace)
	}
	return limit, nil
}

func WithMeter(ctx context.Context, sink func(Usage)) context.Context {
	return context.WithValue(ctx, _MeterKey{}, sink)
}

func reportUsage(ctx context.Context, result _Outcome) {
	sink, metered := ctx.Value(_MeterKey{}).(func(Usage))
	if !metered {
		return
	}
	switch settled := result.(type) {
	case _Answered:
		sink(settled.used)
	case _Refused:
		if settled.used != nil {
			sink(*settled.used)
		}
	}
}

type _TimeExhausted struct {
	limit    time.Duration
	building bool
}

func (c _TimeExhausted) Error() string {
	if c.building {
		return fmt.Sprintf("the build exceeded its time limit of %s", c.limit)
	}
	return fmt.Sprintf("exceeded the time limit of %s", c.limit)
}

func (c _TimeExhausted) Is(target error) bool {
	return target == TimeLimit
}

func exhaustedCause(ctx context.Context) (error, bool) {
	switch cause := context.Cause(ctx).(type) {
	case _WallExhausted, _TimeExhausted:
		return cause, true
	}
	return nil, false
}

type _Meter struct {
	thread    _ThreadClock
	cpuStart  time.Duration
	wallStart time.Time
}

func startMeter() *_Meter {
	return &_Meter{thread: currentThreadClock(), cpuStart: ownThreadCPU(), wallStart: time.Now()}
}

func (m *_Meter) used() Usage {
	return Usage{CPU: ownThreadCPU() - m.cpuStart, Wall: time.Since(m.wallStart)}
}

func (m *_Meter) observed() Usage {
	var cpu, _ = m.thread.cpu()
	if cpu != 0 {
		cpu -= m.cpuStart
	}
	return Usage{CPU: cpu, Wall: time.Since(m.wallStart)}
}

func (m *_Meter) spent() time.Duration {
	if cpu, measured := m.thread.cpu(); measured {
		return cpu - m.cpuStart
	}
	return time.Since(m.wallStart)
}

func (l _TimeLimit) enforce(ctx context.Context, meter *_Meter, building bool) (context.Context, context.CancelFunc) {
	if l == 0 {
		return ctx, func() {}
	}
	bounded, cancel := context.WithCancelCause(ctx)
	go l.watch(bounded, meter, func() { cancel(_TimeExhausted{limit: time.Duration(l), building: building}) })
	return bounded, func() { cancel(context.Canceled) }
}

func (l _TimeLimit) watch(ctx context.Context, meter *_Meter, exhausted func()) {
	var timer = time.NewTimer(0)
	defer timer.Stop()
	for {
		var spent = meter.spent()
		if spent >= time.Duration(l) {
			exhausted()
			return
		}
		timer.Reset(max(time.Duration(l)-spent, cpuPollFloor))
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func onALockedThread(work func()) {
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	work()
}
