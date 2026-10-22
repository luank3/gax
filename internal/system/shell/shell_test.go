package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindUsesOnlyAbsolutePATHEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell lookup follows POSIX PATH semantics")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	relativeBin := filepath.Join(root, "relative-bin")
	for _, directory := range []string{bin, relativeBin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{bin, relativeBin} {
		if err := os.WriteFile(filepath.Join(directory, "fixture-shell"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("PATH", "relative-bin")
	if got := Find("fixture-shell"); got != "" {
		t.Fatalf("Find() with relative PATH entry = %q, want no match", got)
	}
	t.Setenv("PATH", bin+":relative-bin")
	if got, want := Find("fixture-shell"), filepath.Join(bin, "fixture-shell"); got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindAcceptsExecutableConfiguredPath(t *testing.T) {
	name := "custom-shell"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Find(path); got != path {
		t.Fatalf("Find(absolute shell) = %q, want %q", got, path)
	}
}

func TestFindRequiresExecutableRegularFile(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Find(path); got != "" {
		t.Fatalf("Find(directory) = %q, want no match", got)
	}
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Find(nonExecutable); got != "" {
		t.Fatalf("Find(non-executable) = %q, want no match", got)
	}
}

func TestResolveRejectsRelativePathShellAndReportsInvalidConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell lookup follows POSIX paths")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "relative-bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "relative-bin", "bash"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("PATH", "relative-bin")
	got, invalid := Resolve("missing-configured-shell")
	want := "/bin/sh"
	if info, err := os.Stat("/bin/bash"); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
		want = "/bin/bash"
	}
	if got != want || !invalid {
		t.Fatalf("Resolve() = (%q, %t), want (%q, true)", got, invalid, want)
	}
}
