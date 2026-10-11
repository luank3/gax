//go:build !linux

package input

import (
	"os"
)

func openHistoryFile(path string, flags int, mode os.FileMode) *os.File {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
		return nil
	}
	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return nil
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil
	}
	return file
}
