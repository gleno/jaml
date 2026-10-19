package jaml

import (
	"os"
	"syscall"
)

func peakResident(state *os.ProcessState) (uint64, bool) {
	usage, isUsage := state.SysUsage().(*syscall.Rusage)
	if !isUsage {
		return 0, false
	}
	return uint64(usage.Maxrss) << 10, true
}
