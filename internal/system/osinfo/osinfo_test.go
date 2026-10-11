package osinfo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/system/osinfo"
)

func TestReleaseNameReadsPrettyNameAndFallsBackToNameAndVersion(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "pretty name", data: "NAME=ignored\nPRETTY_NAME=\"Test Linux 1.0\"\n", want: "Test Linux 1.0"},
		{name: "name and version", data: "NAME='Test Linux'\nVERSION=\"2 (Tree)\"\n", want: "Test Linux 2 (Tree)"},
		{name: "quoted escapes", data: "PRETTY_NAME=\"Test \\\"Linux\\\"\"\n", want: `Test "Linux"`},
		{name: "single quotes keep backslashes", data: "PRETTY_NAME='Test \\\"Linux\\\"'\n", want: `Test \"Linux\"`},
		{name: "duplicate keys use last value", data: "PRETTY_NAME=old\nPRETTY_NAME=new\n", want: "new"},
		{name: "missing identity", data: "ID=test\n", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "os-release")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			if got := osinfo.ReleaseName(path); got != test.want {
				t.Fatalf("ReleaseName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReleaseNameWithFallbackUsesFallbackOnlyWhenPrimaryIsMissing(t *testing.T) {
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary")
	fallback := filepath.Join(directory, "fallback")
	if err := os.WriteFile(primary, []byte("ID=override\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallback, []byte("PRETTY_NAME=fallback\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := osinfo.ReleaseNameWithFallback(primary, fallback); got != "" {
		t.Fatalf("existing primary without an identity fell through to fallback: %q", got)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if got := osinfo.ReleaseNameWithFallback(primary, fallback); got != "fallback" {
		t.Fatalf("missing primary fallback = %q, want fallback", got)
	}
	notDirectory := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := osinfo.ReleaseNameWithFallback(filepath.Join(notDirectory, "os-release"), fallback); got != "fallback" {
		t.Fatalf("ENOTDIR primary fallback = %q, want fallback", got)
	}
}

func TestReleaseNameRejectsFilesOver64KiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	data := []byte("PRETTY_NAME=too large\n" + strings.Repeat("x", 64*1024))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := osinfo.ReleaseName(path); got != "" {
		t.Fatalf("oversized os-release name = %q, want empty", got)
	}
}

func TestDescriptionReturnsAValue(t *testing.T) {
	if got := osinfo.Description(); got == "" {
		t.Fatal("Description() returned an empty host description")
	}
}
