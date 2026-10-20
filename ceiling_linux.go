//go:build linux && !race

package jaml

import (
	"bytes"
	"fmt"
	"os"
	goruntime "runtime"
	"strconv"
	"syscall"
)

const (
	addressGrowthPerLimit = 3
	addressSlack          = 256 << 20
	addressPerThread      = 8 << 20
)

func confineTheAddressSpace(limit _MemoryLimit) error {
	held, err := addressSpaceHeld()
	if err != nil {
		return fmt.Errorf("reading the address space: %w", err)
	}
	var ceiling = held + addressGrowthPerLimit*uint64(limit) + addressSlack + addressPerThread*uint64(goruntime.GOMAXPROCS(0))
	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &current); err != nil {
		return fmt.Errorf("reading the address space limit: %w", err)
	}
	if ceiling >= current.Cur {
		return nil
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: ceiling, Max: min(ceiling, current.Max)}); err != nil {
		return fmt.Errorf("limiting the address space to %d bytes: %w", ceiling, err)
	}
	return nil
}

func addressSpaceHeld() (uint64, error) {
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	var fields = bytes.Fields(statm)
	if len(fields) == 0 {
		return 0, fmt.Errorf("an empty /proc/self/statm")
	}
	pages, err := strconv.ParseUint(string(fields[0]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("/proc/self/statm: %w", err)
	}
	return pages * uint64(os.Getpagesize()), nil
}
