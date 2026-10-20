//go:build !linux && !darwin

package jaml

import "time"

type _ThreadClock struct{}

func currentThreadClock() _ThreadClock {
	return _ThreadClock{}
}

func (_ThreadClock) cpu() (time.Duration, bool) {
	return 0, false
}

func ownThreadCPU() time.Duration {
	return 0
}
