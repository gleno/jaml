package jaml

import (
	"context"
	"fmt"
	goruntime "runtime"
	"time"
)

const cpuPollFloor = time.Millisecond

type _CPULimitKey struct{}

type _CPULimit time.Duration

type _MeterKey struct{}

type Usage struct {
	CPU  time.Duration
	Wall time.Duration
}

func WithCPULimit(ctx context.Context, limit time.Duration) context.Context {
	if earlier, limited := ctx.Value(_CPULimitKey{}).(_CPULimit); limited && time.Duration(earlier) <= limit {
		return ctx
	}
	return context.WithValue(ctx, _CPULimitKey{}, _CPULimit(limit))
}

func cpuLimitOf(ctx context.Context) (_CPULimit, error) {
	limit, limited := ctx.Value(_CPULimitKey{}).(_CPULimit)
	if limited && limit <= 0 {
		return 0, fail(invalidInput, "a CPU time limit must be positive, got %s", time.Duration(limit)).at(rootPlace)
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

type _CPUExhausted struct {
	limit    time.Duration
	building bool
}

func (c _CPUExhausted) Error() string {
	if c.building {
		return fmt.Sprintf("the build exceeded its CPU time limit of %s", c.limit)
	}
	return fmt.Sprintf("exceeded the CPU time limit of %s", c.limit)
}

func (c _CPUExhausted) Is(target error) bool {
	return target == CPULimit
}

func exhaustedCause(ctx context.Context) (error, bool) {
	switch cause := context.Cause(ctx).(type) {
	case _TimeExhausted, _CPUExhausted:
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

func (m *_Meter) spent() time.Duration {
	if cpu, measured := m.thread.cpu(); measured {
		return cpu - m.cpuStart
	}
	return time.Since(m.wallStart)
}

func (l _CPULimit) enforce(ctx context.Context, meter *_Meter, building bool) (context.Context, context.CancelFunc) {
	if l == 0 {
		return ctx, func() {}
	}
	bounded, cancel := context.WithCancelCause(ctx)
	go l.watch(bounded, meter, func() { cancel(_CPUExhausted{limit: time.Duration(l), building: building}) })
	return bounded, func() { cancel(context.Canceled) }
}

func (l _CPULimit) watch(ctx context.Context, meter *_Meter, exhausted func()) {
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
