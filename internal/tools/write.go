package tools

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	systemfs "github.com/luank3/gax/internal/system/fs"
	systempath "github.com/luank3/gax/internal/system/path"
)

func writeWithDiff(path string, content []byte) (created bool, diff string, err error) {
	target, err := resolveWriteTarget(path)
	if err != nil {
		return false, "", fmt.Errorf("resolving %s: %w", path, err)
	}
	info, statErr := os.Stat(target)
	existed := statErr == nil
	mode := os.FileMode(0o644)
	var original []byte
	if existed {
		if !info.Mode().IsRegular() {
			return false, "", fmt.Errorf("%s exists but is not a regular file", path)
		}
		mode = info.Mode().Perm() | info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
		original, err = systemfs.ReadFile(target)
		if err != nil {
			return false, "", fmt.Errorf("error reading %s: %w", path, err)
		}
		if bytes.Equal(original, content) {
			return false, "", nil
		}
	} else if !os.IsNotExist(statErr) {
		return false, "", fmt.Errorf("stat %s: %w", path, statErr)
	}

	oldLabel, newLabel := diffLabels(path, existed)
	diff = unifiedDiff(original, content, oldLabel, newLabel)
	parent := systempath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return false, "", fmt.Errorf("creating %s: %w", parent, err)
	}
	temporaryDir, err := os.MkdirTemp(parent, ".hax-write-*")
	if err != nil {
		return false, "", fmt.Errorf("mkstemp in %s: %w", parent, err)
	}
	defer os.RemoveAll(temporaryDir)
	temporaryPath := systempath.Join(temporaryDir, "content")
	temporary, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return false, "", fmt.Errorf("create %s: %w", temporaryPath, err)
	}
	if !existed {
		if temporaryInfo, statErr := temporary.Stat(); statErr == nil {
			mode = temporaryInfo.Mode().Perm()
		}
	}
	if _, err = io.Copy(temporary, bytes.NewReader(content)); err != nil {
		_ = temporary.Close()
		return false, "", fmt.Errorf("write %s: %w", temporaryPath, err)
	}
	if err = temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return false, "", fmt.Errorf("chmod %s: %w", temporaryPath, err)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, "", fmt.Errorf("fsync %s: %w", temporaryPath, err)
	}
	if err = temporary.Close(); err != nil {
		return false, "", fmt.Errorf("close %s: %w", temporaryPath, err)
	}
	if err = os.Rename(temporaryPath, target); err != nil {
		return false, "", fmt.Errorf("rename to %s: %w", target, err)
	}
	return !existed, diff, nil
}

func resolveWriteTarget(path string) (string, error) {
	current := path
	for range 32 {
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return current, nil
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return current, nil
		}
		target, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = systempath.Join(systempath.Dir(current), target)
		}
		current = target
	}
	return "", fmt.Errorf("too many symbolic links")
}

func diffLabels(path string, existed bool) (oldLabel, newLabel string) {
	if !existed {
		oldLabel = "/dev/null"
	} else if filepath.IsAbs(path) {
		oldLabel = path
	} else {
		oldLabel = "a/" + path
	}
	if filepath.IsAbs(path) {
		newLabel = path
	} else {
		newLabel = "b/" + path
	}
	return oldLabel, newLabel
}
