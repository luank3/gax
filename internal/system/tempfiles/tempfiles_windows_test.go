//go:build windows

package tempfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsTempDefault(t *testing.T) {
	isolateTempfiles(t)
	root := t.TempDir()
	t.Setenv("TMPDIR", "")
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	file, path, err := Create("bash-", ".log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if filepath.Dir(filepath.Dir(path)) != os.TempDir() {
		t.Errorf("Create() = %q, want under %q", path, os.TempDir())
	}
	for _, fragment := range []string{`..\escape`, `a/b`} {
		if file, _, err := Create(fragment, ""); err == nil {
			file.Close()
			t.Errorf("Create(%q) succeeded", fragment)
		}
	}
}
