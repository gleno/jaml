//go:build !(unix && !ios)

package jaml

import (
	goruntime "runtime"
	"syscall"
)

func isolationSupported() error {
	return fail(Unsupported, "an isolated sandbox needs a Unix process model that can start itself again; %s is not supported", goruntime.GOOS)
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return nil
}
