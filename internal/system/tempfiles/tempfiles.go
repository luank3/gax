// Package tempfiles creates private, process-tracked temporary files.
package tempfiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

const maxCreateAttempts = 100

var files struct {
	sync.Mutex
	tracked   []string
	retired   []string
	directory string
	tmpdir    string
	nextID    uint64
}

// Create makes a 0600 file inside a private gax-* directory and tracks it for Cleanup.
// Prefix and suffix are literal filename fragments and may not contain path separators.
func Create(prefix, suffix string) (*os.File, string, error) {
	if !validFragment(prefix) || !validFragment(suffix) {
		return nil, "", syscall.EINVAL
	}

	files.Lock()
	defer files.Unlock()
	directory, err := ensureDirectoryLocked()
	if err != nil {
		return nil, "", err
	}
	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		files.nextID++
		path := filepath.Join(directory, fmt.Sprintf("%s%d%s", prefix, files.nextID, suffix))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			files.tracked = append(files.tracked, path)
			return file, path, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			files.directory = ""
			files.tmpdir = ""
			directory, err = ensureDirectoryLocked()
			if err != nil {
				return nil, "", err
			}
			continue
		}
		return nil, "", err
	}
	return nil, "", syscall.EEXIST
}

// Untrack leaves path in place and removes it from process-exit cleanup ownership.
func Untrack(path string) {
	files.Lock()
	defer files.Unlock()
	for index, tracked := range files.tracked {
		if tracked == path {
			last := len(files.tracked) - 1
			files.tracked[index] = files.tracked[last]
			files.tracked = files.tracked[:last]
			return
		}
	}
}

// Cleanup removes tracked files and then removes empty active and retired directories. A
// nonempty directory is retained so a later call can retry after its remaining files disappear.
func Cleanup() error {
	files.Lock()
	defer files.Unlock()
	var cleanupErr error
	for _, path := range files.tracked {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	files.tracked = nil

	for index := len(files.retired) - 1; index >= 0; index-- {
		if err := removeEmptyDirectory(files.retired[index]); err == nil || errors.Is(err, os.ErrNotExist) {
			last := len(files.retired) - 1
			files.retired[index] = files.retired[last]
			files.retired = files.retired[:last]
		}
	}
	if files.directory != "" {
		if err := removeEmptyDirectory(files.directory); err == nil || errors.Is(err, os.ErrNotExist) {
			files.directory = ""
			files.tmpdir = ""
		}
	}
	return cleanupErr
}

func validFragment(fragment string) bool {
	return utf8.ValidString(fragment) && !strings.ContainsRune(fragment, '/')
}

func ensureDirectoryLocked() (string, error) {
	tmpdir := os.Getenv("TMPDIR")
	if tmpdir == "" || !utf8.ValidString(tmpdir) {
		tmpdir = "/tmp"
	}
	if files.directory != "" && files.tmpdir == tmpdir {
		return files.directory, nil
	}
	newDirectory, err := os.MkdirTemp(tmpdir, "gax-")
	if err != nil {
		return "", err
	}
	if files.directory != "" {
		files.retired = append(files.retired, files.directory)
	}
	files.directory = newDirectory
	files.tmpdir = tmpdir
	return newDirectory, nil
}

func removeEmptyDirectory(path string) error {
	return os.Remove(path)
}
