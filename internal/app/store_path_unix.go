//go:build !windows

package app

import (
	"errors"
	"os"
	"syscall"
)

func missingStoreFile(_ string, err error) bool {
	return errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR)
}
