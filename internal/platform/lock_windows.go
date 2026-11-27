//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

type windowsFileLock struct {
	file *os.File
}

func lockFile(path string) (FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &windowsFileLock{file: f}, nil
}

func (l *windowsFileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	var ol windows.Overlapped
	err1 := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &ol)
	err2 := l.file.Close()
	l.file = nil
	if err1 != nil {
		return err1
	}
	return err2
}
