//go:build unix && !ios

package jaml

import (
	"os"
	"syscall"
)

func isolationSupported() error {
	return nil
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killedOutright(state *os.ProcessState) bool {
	status, isStatus := state.Sys().(syscall.WaitStatus)
	return isStatus && status.Signaled() && status.Signal() == syscall.SIGKILL
}
