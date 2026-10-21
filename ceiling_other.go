//go:build !linux || race

package jaml

func confineTheAddressSpace(_MemoryLimit, int) (uint64, error) {
	return 0, nil
}
