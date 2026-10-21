package jaml

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

func peakResident(state *os.ProcessState) (uint64, bool) {
	usage, isUsage := state.SysUsage().(*syscall.Rusage)
	if !isUsage {
		return 0, false
	}
	return uint64(usage.Maxrss) << 10, true
}

func residentBytes() int64 {
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	var fields = bytes.Fields(statm)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}
