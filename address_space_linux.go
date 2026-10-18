package jaml

import (
	"fmt"
	"os"
	"syscall"
)

const addressSpaceFloor = 4 << 30

func limitAddressSpace(limit int64) {
	ceiling := max(uint64(limit)*8, addressSpaceFloor)
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: ceiling, Max: ceiling}); err != nil {
		fmt.Fprintf(os.Stderr, "jaml evaluator: address space backstop not set: %v\n", err)
	}
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
