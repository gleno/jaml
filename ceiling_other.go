//go:build !linux || race

package jaml

func confineTheAddressSpace(_MemoryLimit, int) error {
	return nil
}
