//go:build !windows

package fs

import (
	"os"
	"syscall"
	"time"
)

// OpenNoFollow opens a file without following a final symlink or blocking on special files.
func OpenNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Lock holds an advisory lock until Unlock or file close.
func Lock(file *os.File, exclusive, nonblocking bool) error {
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	if nonblocking {
		operation |= syscall.LOCK_NB
	}
	return syscall.Flock(int(file.Fd()), operation)
}

// Unlock releases a lock held by this file handle.
func Unlock(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }

// TouchFile changes timestamps through the open handle, not a potentially replaced path.
func TouchFile(file *os.File, now time.Time) error {
	stamp := syscall.NsecToTimeval(now.UnixNano())
	return syscall.Futimes(int(file.Fd()), []syscall.Timeval{stamp, stamp})
}

// HasLinks reports whether the open file still has a directory entry.
func HasLinks(_ *os.File, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 0
}

// SyncDirectory persists directory entries after an atomic replacement.
func SyncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// OpenForTouch opens a no-follow file for handle-based timestamp updates.
func OpenForTouch(path string) (*os.File, error) { return OpenNoFollow(path, os.O_RDONLY, 0) }
