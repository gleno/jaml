package jaml

import (
	"time"

	"golang.org/x/sys/unix"
)

type _ThreadClock struct {
	id int32
}

func currentThreadClock() _ThreadClock {
	return _ThreadClock{id: (^int32(unix.Gettid()))<<3 | 6}
}

func (c _ThreadClock) cpu() (time.Duration, bool) {
	return readClock(c.id)
}

func ownThreadCPU() time.Duration {
	var cpu, _ = readClock(unix.CLOCK_THREAD_CPUTIME_ID)
	return cpu
}

func readClock(id int32) (time.Duration, bool) {
	var now unix.Timespec
	if err := unix.ClockGettime(id, &now); err != nil {
		return 0, false
	}
	return time.Duration(now.Nano()), true
}
