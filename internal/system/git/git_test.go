package git

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFindWorktreeRootFindsNearestGitMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "work", "subdir")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if got := FindWorktreeRoot(nested); got != root {
		t.Fatalf("FindWorktreeRoot(%q) = %q, want %q", nested, got, root)
	}
	if got := FindWorktreeRoot(t.TempDir()); got != "" {
		t.Fatalf("FindWorktreeRoot outside a repository = %q, want empty", got)
	}
}

func TestFindWorktreeRootRecognizesFileMarkerAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /tmp/worktree"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := FindWorktreeRoot(root); got != root {
		t.Fatalf("worktree root = %q, want %q", got, root)
	}

	target := t.TempDir()
	symlinkRoot := t.TempDir()
	if err := os.Symlink(target, filepath.Join(symlinkRoot, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := FindWorktreeRoot(symlinkRoot); got != symlinkRoot {
		t.Fatalf("symlink marker root = %q, want %q", got, symlinkRoot)
	}
}

func TestFindWorktreeRootIsNotLimitedToShallowTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	nested := root
	for range 40 {
		nested = filepath.Join(nested, "d")
	}
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if got := FindWorktreeRoot(nested); got != root {
		t.Fatalf("deep worktree root = %q, want %q", got, root)
	}
}

func TestStateSkipsGitOutsideARepository(t *testing.T) {
	calls := 0
	branch, commit, subject := stateWithRunner(t.TempDir(), false, func(string, ...string) *string {
		calls++
		return nil
	})
	if calls != 0 || branch != nil || commit != nil || subject != nil {
		t.Fatalf("outside-repository state = %v/%v/%v after %d git calls", branch, commit, subject, calls)
	}
}

func TestStateCapturesBranchAndHeadWithGitDirOverride(t *testing.T) {
	var calls [][]string
	branch, commit, subject := stateWithRunner(t.TempDir(), true, func(_ string, arguments ...string) *string {
		calls = append(calls, append([]string(nil), arguments...))
		if reflect.DeepEqual(arguments, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}) {
			return stringPtr("feature/picker")
		}
		return stringPtr("abc123\nAdd picker labels")
	})
	if branch == nil || *branch != "feature/picker" || commit == nil || *commit != "abc123" ||
		subject == nil || *subject != "Add picker labels" {
		t.Fatalf("git state = %v/%v/%v", branch, commit, subject)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], []string{"log", "-1", "--format=%h%n%s"}) {
		t.Fatalf("git calls = %v", calls)
	}
}

func TestStateKeepsBranchWhenHeadLookupFails(t *testing.T) {
	branch, commit, subject := stateWithRunner(t.TempDir(), true, func(_ string, arguments ...string) *string {
		if strings.HasPrefix(arguments[0], "symbolic-ref") {
			return stringPtr("feature/picker")
		}
		return nil
	})
	if branch == nil || *branch != "feature/picker" || commit != nil || subject != nil {
		t.Fatalf("partial git state = %v/%v/%v", branch, commit, subject)
	}
}

func TestRunGitRejectsOutputOverLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		outputLen int
		wantNil   bool
	}{
		{name: "at limit", outputLen: probeLimit},
		{name: "over limit", outputLen: probeLimit + 1, wantNil: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			git := "#!/bin/sh\nprintf '" + strings.Repeat("x", test.outputLen) + "'\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)

			got := runGit(t.TempDir())
			if test.wantNil {
				if got != nil {
					t.Fatalf("runGit returned %d bytes for %d-byte output, want failure", len(*got), test.outputLen)
				}
				return
			}
			if got == nil || len(*got) != test.outputLen {
				t.Fatalf("runGit output length = %v, want %d bytes", got, test.outputLen)
			}
		})
	}
}
