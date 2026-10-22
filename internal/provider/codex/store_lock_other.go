//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package codex

import (
	"os"
	"sync"

	systempath "github.com/luank3/gax/internal/system/path"
)

var authStoreMutex sync.Mutex

func lockAuthStore(path string) (func(), error) {
	if err := os.MkdirAll(systempath.Dir(path), 0700); err != nil {
		return nil, err
	}
	authStoreMutex.Lock()
	return authStoreMutex.Unlock, nil
}
