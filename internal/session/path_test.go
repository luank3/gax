package session

import (
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
	want := "/tmp/state//hax/sessions/" + EncodeCWD(cwd)
	if got != want {
		t.Fatalf("Directory() = %q, want %q", got, want)
	}
}
