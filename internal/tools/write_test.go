package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	systempath "github.com/luank3/gax/internal/system/path"
)

func TestResolveWriteTargetPreservesSymlinkTargetComponents(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "link")
	target := strings.Repeat("missing/../", 30) + "target"
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := resolveWriteTarget(link)
	if err != nil {
		t.Fatal(err)
	}
	want := systempath.Join(directory, target)
	if got != want {
		t.Fatalf("resolveWriteTarget() = %q, want %q", got, want)
	}
}
