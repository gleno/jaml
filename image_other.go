//go:build !linux

package jaml

import "os"

func selfImage() (string, error) {
	return os.Executable()
}
