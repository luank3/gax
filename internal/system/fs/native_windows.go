//go:build windows

package fs

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// OpenNoFollow opens a disk file without following a final reparse point.
func OpenNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	return openNoFollow(path, flags, mode, 0)
}

func openNoFollow(path string, flags int, _ os.FileMode, extraAccess uint32) (*os.File, error) {
	if strings.HasPrefix(strings.ToLower(path), `\\.\`) || strings.HasPrefix(strings.ToLower(path), `\\?\globalroot\`) {
		return nil, os.ErrInvalid
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ)
	if flags&os.O_WRONLY != 0 {
		access = syscall.GENERIC_WRITE
	}
	if flags&os.O_RDWR != 0 {
		access = syscall.GENERIC_READ | syscall.GENERIC_WRITE
	}
	if flags&os.O_APPEND != 0 {
		access &^= syscall.GENERIC_WRITE
		access |= syscall.FILE_APPEND_DATA | syscall.FILE_WRITE_ATTRIBUTES
	}
	disposition := uint32(syscall.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		disposition = syscall.OPEN_ALWAYS
		if flags&os.O_EXCL != 0 {
			disposition = syscall.CREATE_NEW
		}
	}
	handle, err := syscall.CreateFile(name, access|extraAccess, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, disposition, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	var info syscall.ByHandleFileInformation
	err = syscall.GetFileInformationByHandle(handle, &info)
	fileType, typeErr := syscall.GetFileType(handle)
	if err == nil && (info.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 || typeErr != nil || fileType != syscall.FILE_TYPE_DISK) {
		err = errors.New("session path is not a regular file or is a reparse point")
	}
	if err == nil && flags&os.O_TRUNC != 0 {
		err = file.Truncate(0)
	}
	if err != nil {
		file.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return file, nil
}

// Lock holds an interprocess lock outside the file's data range until Unlock or close.
func Lock(file *os.File, exclusive, nonblocking bool) error {
	flags := uintptr(0)
	if exclusive {
		flags |= 2
	}
	if nonblocking {
		flags |= 1
	}
	// Unlike flock, Windows byte-range locks affect IO. Reserve a byte beyond any session data.
	overlapped := syscall.Overlapped{Offset: 0xfffffffe, OffsetHigh: 0x7fffffff}
	result, _, err := lockFileEx.Call(file.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

// Unlock releases a lock held by this file handle.
func Unlock(file *os.File) error {
	overlapped := syscall.Overlapped{Offset: 0xfffffffe, OffsetHigh: 0x7fffffff}
	result, _, err := unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

// TouchFile updates timestamps through the open file handle.
func TouchFile(file *os.File, now time.Time) error {
	stamp := syscall.NsecToFiletime(now.UnixNano())
	return syscall.SetFileTime(syscall.Handle(file.Fd()), nil, &stamp, &stamp)
}

// HasLinks reports whether the open file still has a directory entry.
func HasLinks(file *os.File, _ os.FileInfo) bool {
	var info syscall.ByHandleFileInformation
	return syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info) == nil && info.NumberOfLinks > 0
}

// SyncDirectory is a no-op: Windows does not support FlushFileBuffers on directory handles.
// Callers flush the replacement file before renaming it.
func SyncDirectory(string) error { return nil }

// OpenForTouch opens a file with timestamp-write access without requiring data-write access.
func OpenForTouch(path string) (*os.File, error) {
	return openNoFollow(path, os.O_RDONLY, 0, syscall.FILE_WRITE_ATTRIBUTES)
}
