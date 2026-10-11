package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	systempath "github.com/luank3/gax/internal/system/path"
)

func TestResolveLinkTargetPreservesSymlinkTargetComponents(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "link")
	target := strings.Repeat("missing/../", 30) + "target"
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := resolveLinkTarget(link)
	if err != nil {
		t.Fatal(err)
	}
	want := systempath.Join(directory, target)
	if got != want {
		t.Fatalf("resolveLinkTarget() = %q, want %q", got, want)
	}
}

func TestXDGGaxPathPreservesEnvironmentPathText(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config/")
	t.Setenv("HOME", "/home/alice")
	got, err := xdgGaxPath("XDG_CONFIG_HOME", ".config", "nested/../config.json")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/config//gax/nested/../config.json"; got != want {
		t.Fatalf("xdgGaxPath() = %q, want %q", got, want)
	}
}
