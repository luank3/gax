//go:build windows || linux || darwin || freebsd || openbsd || netbsd || dragonfly

package codex

import (
	"os"

	systemfs "github.com/luank3/gax/internal/system/fs"
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
	if err := systemfs.Lock(file, true, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = systemfs.Unlock(file)
		_ = file.Close()
	}, nil
}
