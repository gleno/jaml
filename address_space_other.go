//go:build !linux

package jaml

import "syscall"

func limitAddressSpace(int64) {}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return nil
}
