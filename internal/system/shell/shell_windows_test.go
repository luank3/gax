//go:build windows

package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsFindUsesAbsolutePATHAndExecutableExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+`;.;relative`)
	t.Setenv("PATHEXT", ".EXE;.CMD")
	if got := Find("fixture"); got != path {
		t.Errorf("Find(fixture) = %q, want %q", got, path)
	}
	if got := Find(path); got != path {
		t.Errorf("Find(absolute exe) = %q, want %q", got, path)
	}
	t.Chdir(dir)
	t.Setenv("PATH", ".;relative")
	if got := Find("fixture"); got != "" {
		t.Errorf("Find(relative PATH) = %q, want empty", got)
	}
}
