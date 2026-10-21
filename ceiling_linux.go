//go:build linux && !race

package jaml

import (
	"bytes"
	"fmt"
	"os"
	goruntime "runtime"
	"strconv"
	"sync"
	"syscall"
)

const (
	addressGrowthPerLimit = 3
	addressSlack          = 256 << 20
)

func confineTheAddressSpace(limit _MemoryLimit, jobs int) (uint64, error) {
	createTheThreads(goruntime.GOMAXPROCS(0) + jobs)
	held, err := addressSpaceHeld()
	if err != nil {
		return 0, fmt.Errorf("reading the address space: %w", err)
	}
	var ceiling = held + addressGrowthPerLimit*uint64(limit) + addressSlack
	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &current); err != nil {
		return 0, fmt.Errorf("reading the address space limit: %w", err)
	}
	if ceiling >= current.Cur {
		return current.Cur, nil
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: ceiling, Max: min(ceiling, current.Max)}); err != nil {
		return 0, fmt.Errorf("limiting the address space to %d bytes: %w", ceiling, err)
	}
	return ceiling, nil
}

func createTheThreads(count int) {
	var created, release sync.WaitGroup
	created.Add(count)
	release.Add(1)
	for range count {
		go func() {
			goruntime.LockOSThread()
			defer goruntime.UnlockOSThread()
			created.Done()
			release.Wait()
		}()
	}
	created.Wait()
	release.Done()
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
