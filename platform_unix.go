//go:build !windows

package jaml

func secureTemplatesSupported() error {
	return nil
}
