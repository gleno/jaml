//go:build !linux || race

package jaml

func confineTheAddressSpace(_MemoryLimit) error {
	return nil
}
