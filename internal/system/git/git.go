// Package git contains bounded Git probes used for session and prompt metadata.
package git

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luank3/gax/internal/system/spawn"
)

const (
	probeTimeout = time.Second
	probeLimit   = 8192
)

// FindWorktreeRoot walks upward until it finds a .git entry. Worktrees use a file here, so the
// entry's type is intentionally not restricted. Lstat matches Git's own marker check without
// following a potentially stalled symlink.
func FindWorktreeRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// State reports the current branch, short commit, and commit subject. It skips spawning Git when
// the working directory has no repository marker, unless GIT_DIR explicitly selects a repository.
func State(cwd string) (branch, commit, subject *string) {
	_, gitDirConfigured := os.LookupEnv("GIT_DIR")
	return stateWithRunner(cwd, gitDirConfigured, runGit)
}

func stateWithRunner(cwd string, gitDirConfigured bool, run func(string, ...string) *string) (branch, commit, subject *string) {
	if cwd == "" || run == nil || !gitDirConfigured && FindWorktreeRoot(cwd) == "" {
		return nil, nil, nil
	}
	branch = run(cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	head := run(cwd, "log", "-1", "--format=%h%n%s")
	if head == nil {
		return branch, nil, nil
	}
	parts := strings.SplitN(*head, "\n", 2)
	if parts[0] != "" {
		commit = stringPtr(parts[0])
	}
	if len(parts) == 2 {
		subject = stringPtr(parts[1])
	}
	return branch, commit, subject
}

func runGit(cwd string, arguments ...string) *string {
	argv := append([]string{"git"}, arguments...)
	output, err := spawn.CaptureStdoutInDir(argv, cwd, probeLimit, probeTimeout)
	if err != nil {
		return nil
	}
	value := strings.TrimRight(string(output), "\r\n")
	if value == "" {
		return nil
	}
	return stringPtr(value)
}

func stringPtr(value string) *string { return &value }
