// Package shell resolves executable shells with hax's POSIX PATH rules.
package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// Find returns name as an executable path, or an empty string when it is not an executable
// regular file. PATH lookup intentionally ignores relative entries, as hax's fs_which does.
func Find(name string) string {
	if name == "" {
		return ""
	}
	if strings.Contains(name, "/") {
		if executableRegularFile(name) {
			return name
		}
		return ""
	}
	for _, directory := range strings.Split(os.Getenv("PATH"), ":") {
		if directory == "" || directory[0] != '/' {
			continue
		}
		candidate := filepath.Join(directory, name)
		if executableRegularFile(candidate) {
			return candidate
		}
	}
	return ""
}

// Resolve returns the configured shell or hax's default fallback. invalidConfigured is true when
// a nonempty configured shell could not be resolved and should be diagnosed once by the caller.
func Resolve(configured string) (path string, invalidConfigured bool) {
	if configured != "" {
		if path := Find(configured); path != "" {
			return path, false
		}
		invalidConfigured = true
	}
	if path := Find("bash"); path != "" {
		return path, invalidConfigured
	}
	if executableRegularFile("/bin/bash") {
		return "/bin/bash", invalidConfigured
	}
	return "/bin/sh", invalidConfigured
}

func executableRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}
