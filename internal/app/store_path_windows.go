//go:build windows

package app

import (
	"errors"
	"os"
	"path/filepath"
)

func missingStoreFile(path string, err error) bool {
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	// Windows aliases ENOTDIR to ERROR_PATH_NOT_FOUND. Inspect parents to distinguish a
	// missing directory (normal on first launch) from a file blocking the configured path.
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		info, err := os.Stat(directory)
		if err == nil {
			return info.IsDir()
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if parent := filepath.Dir(directory); parent == directory {
			return true
		}
	}
}
