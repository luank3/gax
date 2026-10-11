//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fs

import (
	"os"
	"syscall"
)

func openRegular(path string) (*os.File, error) {
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, syscall.EBADF
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		if info.IsDir() {
			return nil, syscall.EISDIR
		}
		return nil, syscall.EINVAL
	}
	return file, nil
}
