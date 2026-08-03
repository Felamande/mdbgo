//go:build !unix

package gomdb

import "os"

func mapFileData(f *os.File, size int64) ([]byte, bool) {
	return nil, false
}

func unmapFileData(data []byte) {}
