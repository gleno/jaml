//go:build !(unix && !ios)

package jaml

import (
	goruntime "runtime"
	"syscall"
)

func secureTemplatesSupported() error {
	return fail(Unsupported, "secure templates need a Unix process model that can start itself again; %s is not supported", goruntime.GOOS)
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return nil
}
