package jaml

import (
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	procInfoOfProcess = 2
	threadInfoByID    = 15
)

type _ThreadClock struct {
	id uint64
}

type _ThreadInfo struct {
	user   uint64
	system uint64
	state  [8]int32
	name   [64]byte
}

func currentThreadClock() _ThreadClock {
	id, _, _ := unix.RawSyscall(unix.SYS_THREAD_SELFID, 0, 0, 0)
	return _ThreadClock{id: uint64(id)}
}

func (c _ThreadClock) cpu() (time.Duration, bool) {
	var info _ThreadInfo
	written, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoOfProcess, uintptr(unix.Getpid()), threadInfoByID, uintptr(c.id), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 || written != unsafe.Sizeof(info) {
		return 0, false
	}
	return time.Duration(info.user + info.system), true
}

func ownThreadCPU() time.Duration {
	var cpu, _ = currentThreadClock().cpu()
	return cpu
}
