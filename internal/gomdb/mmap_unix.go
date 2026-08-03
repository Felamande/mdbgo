//go:build unix

package gomdb

import (
	"os"
	"syscall"
)

// mapFileData maps a file read-only into memory. Returns (nil, false) when the
// platform or file cannot be mapped, so callers can fall back to reading.
func mapFileData(f *os.File, size int64) ([]byte, bool) {
	if f == nil || size <= 0 {
		return nil, false
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, false
	}
	return data, true
}

func unmapFileData(data []byte) {
	_ = syscall.Munmap(data)
}
