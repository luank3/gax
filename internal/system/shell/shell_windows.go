//go:build windows

// Package shell resolves executable command shells.
package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Find resolves a Windows executable without searching relative PATH entries or the current directory.
func Find(name string) string {
	if name == "" {
		return ""
	}
	if strings.ContainsAny(name, `/\`) || filepath.VolumeName(name) != "" {
		if !filepath.IsAbs(name) {
			return ""
		}
		path, err := exec.LookPath(name)
		if err == nil {
			return path
		}
		return ""
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(directory) {
			continue
		}
		if path, err := exec.LookPath(filepath.Join(directory, name)); err == nil {
			return path
		}
	}
	return ""
}

// Resolve selects Git Bash; the Windows WSL bash launcher is not a native shell backend.
func Resolve(configured string) (path string, invalidConfigured bool) {
	if configured != "" {
		if path := Find(configured); path != "" {
			return path, false
		}
		invalidConfigured = true
	}
	if git := Find("git.exe"); git != "" {
		root := filepath.Dir(filepath.Dir(git))
		for _, relative := range []string{`bin\bash.exe`, `usr\bin\bash.exe`} {
			if path := Find(filepath.Join(root, relative)); path != "" {
				return path, invalidConfigured
			}
		}
	}
	if path := Find("bash.exe"); path != "" {
		system32 := filepath.Join(os.Getenv("SystemRoot"), "System32")
		if !strings.EqualFold(filepath.Dir(path), system32) {
			return path, invalidConfigured
		}
	}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs")} {
		if base == "" {
			continue
		}
		if path := Find(filepath.Join(base, "Git", "bin", "bash.exe")); path != "" {
			return path, invalidConfigured
		}
	}
	return "", invalidConfigured
}

// Default returns the shell used for POSIX terminal helper commands.
func Default() string {
	path, _ := Resolve("")
	return path
}

// FindCommand also searches the Git Bash utilities, which need not be on the native PATH.
func FindCommand(name string) string {
	if path := Find(name); path != "" {
		return path
	}
	if strings.ContainsAny(name, `/\`) {
		return ""
	}
	bash, _ := Resolve("")
	if bash == "" {
		return ""
	}
	for _, directory := range []string{filepath.Dir(bash), filepath.Join(filepath.Dir(filepath.Dir(bash)), "usr", "bin")} {
		if path := Find(filepath.Join(directory, name)); path != "" {
			return path
		}
	}
	return ""
}
