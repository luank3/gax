//go:build linux

package input

import (
	"os"
	"syscall"
)

// Open nonblocking before checking the inode so a replaced FIFO cannot stall the editor.
func openHistoryFile(path string, flags int, mode os.FileMode) *os.File {
	fd, err := syscall.Open(path, flags|syscall.O_NONBLOCK|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil
	}
	return file
}
