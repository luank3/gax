//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package codex

import (
	"os"
	"syscall"

	systempath "github.com/luank3/gax/internal/system/path"
)

func lockAuthStore(path string) (func(), error) {
	if err := os.MkdirAll(systempath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
