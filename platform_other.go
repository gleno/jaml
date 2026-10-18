//go:build !(unix && !ios)

package jaml

import (
	"fmt"
	goruntime "runtime"
	"syscall"

	"github.com/gleno/fault"
)

func secureTemplatesSupported() error {
	return fault.From(Unsupported, fmt.Sprintf("secure templates need a Unix process model that can start itself again; %s is not supported", goruntime.GOOS))
}

func evaluatorProcAttr() *syscall.SysProcAttr {
	return nil
}
