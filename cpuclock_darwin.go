package jaml

import (
	"time"

	"golang.org/x/sys/unix"
)

type _ThreadClock struct{}

func currentThreadClock() _ThreadClock {
	return _ThreadClock{}
}

func (_ThreadClock) cpu() (time.Duration, bool) {
	return 0, false
}

func ownThreadCPU() time.Duration {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &now); err != nil {
		return 0
	}
	return time.Duration(now.Nano())
}
