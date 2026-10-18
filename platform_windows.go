package jaml

import "github.com/gleno/fault"

func secureTemplatesSupported() error {
	return fault.From(Unsupported, "secure templates need a Unix process model; windows is not supported")
}
