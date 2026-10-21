//go:build !linux

package jaml

import "os"

func peakResident(*os.ProcessState) (uint64, bool) {
	return 0, false
}

func residentBytes() int64 {
	return 0
}
