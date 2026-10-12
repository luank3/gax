package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromptHistoryPathSharesCWDSessionDirectory(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateRoot)
	path, err := PromptHistoryPath("/work/project")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := Directory("/work/project")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "history") {
		t.Fatalf("PromptHistoryPath() = %q, want sibling history file", path)
	}
	if _, err := PromptHistoryPath(""); err == nil {
		t.Fatal(`PromptHistoryPath("") unexpectedly succeeded`)
	}
}

func TestDirectoryPreservesXDGPathFormatting(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/state/")
	t.Setenv("HOME", "/home/alice")
	cwd := "/work/project"
	got, err := Directory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/state//gax/sessions/" + EncodeCWD(cwd)
	if got != want {
		t.Fatalf("Directory() = %q, want %q", got, want)
	}
}

// One physical directory must map to one bucket: a working directory reached through a symlink
// (macOS hands out /var/folders while os.Getwd reports /private/var) has to encode like the
// directory it resolves to, or a resumed session looks in a bucket the writer never used.
func TestEncodeCWDResolvesSymlinkedWorkingDirectory(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real-project")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-project")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, want := EncodeCWD(link), EncodeCWD(real); got != want {
		t.Fatalf("EncodeCWD(%q) = %q, want the encoding of %q = %q", link, got, real, want)
	}
}
