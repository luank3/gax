package tempfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateTempfiles(t *testing.T) {
	t.Helper()
	if err := Cleanup(); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	t.Cleanup(func() { _ = Cleanup() })
}

func TestCreateUsesPrivateTrackedFilesAndCleanupRemovesThem(t *testing.T) {
	isolateTempfiles(t)
	tmpdir := t.TempDir()
	t.Setenv("TMPDIR", tmpdir)

	plain, plainPath, err := Create("t-", "")
	if err != nil {
		t.Fatal(err)
	}
	image, imagePath, err := Create("t-", ".png")
	if err != nil {
		t.Fatal(err)
	}
	if plainPath == imagePath || filepath.Dir(plainPath) != filepath.Dir(imagePath) {
		t.Fatalf("created paths = %q and %q, want distinct files in one private directory", plainPath, imagePath)
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(plainPath)), "hax-") || !strings.HasPrefix(filepath.Base(plainPath), "t-") ||
		!strings.HasSuffix(imagePath, ".png") {
		t.Fatalf("created names = %q, %q", plainPath, imagePath)
	}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(plainPath))
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode = %v, err=%v", directoryInfo, err)
	}
	for _, path := range []string{plainPath, imagePath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file %q mode = %v, err=%v", path, info, err)
		}
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{plainPath, imagePath, filepath.Dir(plainPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cleanup left %q: stat err=%v", path, err)
		}
	}
}

func TestUntrackLeavesFileAndCleanupRetriesNonemptyDirectory(t *testing.T) {
	isolateTempfiles(t)
	t.Setenv("TMPDIR", t.TempDir())
	file, path, err := Create("keep-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	Untrack(path)
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("untracked file was removed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("empty directory was not removed on retry: err=%v", err)
	}
}

func TestUntrackUnknownPathIsNoop(t *testing.T) {
	isolateTempfiles(t)
	Untrack("/nonexistent/not-tracked")
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestTMPDIRChangeRetiresPreviousDirectory(t *testing.T) {
	isolateTempfiles(t)
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", firstRoot)
	kept, keptPath, err := Create("kept-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := kept.Close(); err != nil {
		t.Fatal(err)
	}
	Untrack(keptPath)
	firstDir := filepath.Dir(keptPath)

	t.Setenv("TMPDIR", secondRoot)
	removed, removedPath, err := Create("removed-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := removed.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(removedPath) == firstDir {
		t.Fatalf("TMPDIR change reused old directory %q", firstDir)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keptPath); err != nil {
		t.Fatalf("cleanup removed untracked file: %v", err)
	}
	if _, err := os.Stat(firstDir); err != nil {
		t.Fatalf("nonempty retired directory was not retained for retry: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(removedPath)); !os.IsNotExist(err) {
		t.Fatalf("tracked file directory was not removed: err=%v", err)
	}
	if err := os.Remove(keptPath); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
		t.Fatalf("retired directory remained after it became empty: err=%v", err)
	}
}

func TestCreateRecoversWhenActiveDirectoryWasRemoved(t *testing.T) {
	isolateTempfiles(t)
	t.Setenv("TMPDIR", t.TempDir())
	first, firstPath, err := Create("first-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstDir := filepath.Dir(firstPath)
	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(firstDir); err != nil {
		t.Fatal(err)
	}
	second, secondPath, err := Create("second-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(secondPath) == firstDir {
		t.Fatalf("creation reused externally removed directory %q", firstDir)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupForgetsExternallyRemovedActiveDirectory(t *testing.T) {
	isolateTempfiles(t)
	t.Setenv("TMPDIR", t.TempDir())
	first, firstPath, err := Create("first-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstDirectory := filepath.Dir(firstPath)
	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(firstDirectory); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}

	second, secondPath, err := Create("second-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(secondPath) == firstDirectory {
		t.Fatalf("creation reused removed active directory %q", firstDirectory)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidUTF8TMPDIRFallsBackToTmp(t *testing.T) {
	isolateTempfiles(t)
	t.Setenv("TMPDIR", "/tmp/invalid-"+string([]byte{0xff}))
	file, path, err := Create("fallback-", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/tmp/hax-") {
		t.Fatalf("invalid UTF-8 TMPDIR path = %q, want /tmp/hax-*", path)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsInvalidNameFragmentsAndReportsTMPDIRError(t *testing.T) {
	isolateTempfiles(t)
	for _, test := range []struct{ prefix, suffix string }{
		{"../escape-", ""}, {"valid-", "/suffix"}, {string([]byte{0xff}), ""}, {"valid-", string([]byte{0xff})},
	} {
		if file, path, err := Create(test.prefix, test.suffix); err == nil {
			if file != nil {
				_ = file.Close()
			}
			t.Errorf("Create(%q, %q) = %q, nil error", test.prefix, test.suffix, path)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing)
	if file, path, err := Create("x-", ""); err == nil {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("Create under missing TMPDIR = %q, nil error", path)
	}
}
