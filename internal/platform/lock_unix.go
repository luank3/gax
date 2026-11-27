//go:build !windows

package platform

import (
	"os"

	"golang.org/x/sys/unix"
)

type unixFileLock struct {
	file *os.File
}

func lockFile(path string) (FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &unixFileLock{file: f}, nil
}

func (l *unixFileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err1 := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	err2 := l.file.Close()
	l.file = nil
	if err1 != nil {
		return err1
	}
	return err2
}
