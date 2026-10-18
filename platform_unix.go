//go:build unix && !ios

package jaml

import "syscall"

func secureTemplatesSupported() error {
	return nil
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
