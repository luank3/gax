package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneBeforeSweepsAllProjectBucketsAndRespectsLocksAndExclusion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	projectA, projectB := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	for _, cwd := range []string{projectA, projectB} {
		if err := os.MkdirAll(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := writePruneTestSession(t, projectA, false)
	recentPath := writePruneTestSession(t, projectA, false)
	excludedPath := writePruneTestSession(t, projectA, false)
	lockedLog := openPruneTestSession(t, projectA)
	lockedPath := lockedLog.Path()
	otherPath := writePruneTestSession(t, projectB, false)
	otherBucket := filepath.Dir(otherPath)
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	for _, path := range []string{oldPath, excludedPath, lockedPath, otherPath} {
		setPruneTestMtime(t, path, oldTime)
	}

	if err := PruneBefore(cutoff, excludedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(excludedPath); err != nil {
		dir := filepath.Dir(excludedPath)
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			info, _ := e.Info()
			names = append(names, e.Name()+" mtime="+info.ModTime().UTC().String())
		}
		t.Fatalf("DIAGCI excluded not a file: %v\nDIR %q\nCWD %q\nENTRIES %v", err, dir, projectA, names)
	}
	assertPruneTestMissing(t, oldPath)
	assertPruneTestMissing(t, otherPath)
	assertPruneTestMissing(t, otherBucket)
	assertPruneTestExists(t, excludedPath)
	assertPruneTestExists(t, lockedPath)
	assertPruneTestExists(t, recentPath)

	if err := lockedLog.Close(); err != nil {
		t.Fatal(err)
	}
	if err := PruneBefore(cutoff, ""); err != nil {
		t.Fatal(err)
	}
	assertPruneTestMissing(t, excludedPath)
	assertPruneTestMissing(t, lockedPath)
	assertPruneTestExists(t, recentPath)
}

func TestPruneBeforeLeavesUnrelatedFilesAndSymlinksAlone(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	target := writePruneTestSession(t, cwd, false)
	bucket := filepath.Dir(target)
	unrelated := filepath.Join(bucket, "backup_00000000-0000-4000-8000-000000000000.jsonl")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bucket, "2000-01-01T00-00-00Z_00000000-0000-4000-8000-000000000001.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	setPruneTestMtime(t, target, oldTime)
	setPruneTestMtime(t, unrelated, oldTime)

	if err := PruneBefore(time.Now().Add(-30*24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	assertPruneTestMissing(t, target)
	assertPruneTestExists(t, unrelated)
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("session-shaped symlink changed: info=%v err=%v", info, err)
	}
}

func TestPruneBeforeDoesNotFollowProjectBucketSymlink(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	project := filepath.Join(t.TempDir(), "project")
	outside := t.TempDir()
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "2000-01-01T00-00-00Z_00000000-0000-4000-8000-000000000001.jsonl")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	setPruneTestMtime(t, target, time.Now().Add(-31*24*time.Hour))
	bucket, err := Directory(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bucket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, bucket); err != nil {
		t.Fatal(err)
	}
	if err := PruneBefore(time.Now().Add(-30*24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	assertPruneTestExists(t, target)
}

func TestTouchProtectsResumedSessionFromPrune(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writePruneTestSession(t, cwd, false)
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	setPruneTestMtime(t, path, oldTime)
	if err := Touch(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().After(oldTime) {
		t.Fatalf("Touch did not refresh session activity: info=%v err=%v", info, err)
	}
	if err := PruneBefore(time.Now().Add(-30*24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	assertPruneTestExists(t, path)
}

func TestListHidesExpiredSessionsUntilRetentionIsDisabled(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := writePruneTestSession(t, cwd, false)
	recentPath := writePruneTestSession(t, cwd, false)
	setPruneTestMtime(t, oldPath, time.Now().Add(-31*24*time.Hour))
	entries, err := List(cwd, 30)
	if err != nil || len(entries) != 1 || entries[0].Path != recentPath {
		t.Fatalf("retained session list = %+v, err=%v", entries, err)
	}
	entries, err = List(cwd, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("disabled retention list = %+v, err=%v", entries, err)
	}
}

func TestStartPruneMarksCompletedSweepAndAppliesDailyThrottle(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	first := writePruneTestSession(t, cwd, false)
	setPruneTestMtime(t, first, oldTime)
	StartPrune(30, "")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(first); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ShutdownPrune()
	assertPruneTestMissing(t, first)
	marker := filepath.Join(stateHome, "gax", "sessions", ".prune")
	markerInfo, err := os.Stat(marker)
	if err != nil || markerInfo.Size() == 0 {
		t.Fatalf("completed sweep marker = %v, err=%v", markerInfo, err)
	}

	second := writePruneTestSession(t, cwd, false)
	setPruneTestMtime(t, second, oldTime)
	StartPrune(30, "")
	ShutdownPrune()
	assertPruneTestExists(t, second)
}

func TestResumeLogDoesNotRecreatePrunedSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writePruneTestSession(t, cwd, false)
	if err := PruneBefore(time.Now().Add(time.Second), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeLog(path, Selection{}, 0); err == nil {
		t.Fatal("ResumeLog recreated a session removed by pruning")
	}
	assertPruneTestMissing(t, path)
}

func TestSessionReadersAndWritersRejectSessionSymlinks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	log := openPruneTestSession(t, cwd)
	path := log.Path()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeLog(path, Selection{}, 0); err == nil {
		t.Fatal("ResumeLog followed a session symlink")
	}
	if _, err := LoadAll(path); err == nil {
		t.Fatal("LoadAll followed a session symlink")
	}
	if err := Touch(path); err == nil {
		t.Fatal("Touch followed a session symlink")
	}
}

func writePruneTestSession(t *testing.T, cwd string, keepOpen bool) string {
	t.Helper()
	log := openPruneTestSession(t, cwd)
	path := log.Path()
	if !keepOpen {
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func openPruneTestSession(t *testing.T, cwd string) *Log {
	t.Helper()
	log, err := NewLog(cwd, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Begin(); err != nil {
		t.Fatal(err)
	}
	return log
}

func setPruneTestMtime(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func assertPruneTestMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%q still exists or could not be inspected: %v", path, err)
	}
}

func assertPruneTestExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%q is missing: %v", path, err)
	}
}

// A sweep must recognise the protected session even when the root it walks is named relative to
// the working directory while the resumed path is absolute; comparing the two path strings alone
// misses, and the sweep then deletes the session the user asked to resume.
func TestPruneTreeKeepsExcludedSessionUnderRelativeRoot(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	// Both sessions are closed before the sweep, as a resumed session is in another process:
	// only the exclusion can keep it, not the writer's shared lock.
	resumed := writePruneTestSession(t, cwd, false)
	resumedLog, err := ResumeLog(resumed, Selection{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumedLog.Close(); err != nil {
		t.Fatal(err)
	}
	expired := writePruneTestSession(t, cwd, false)
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	for _, path := range []string{resumed, expired} {
		setPruneTestMtime(t, path, oldTime)
	}

	sessionsRoot, err := sessionsRootPath()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(stateHome); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	relativeRoot, err := filepath.Rel(stateHome, sessionsRoot)
	if err != nil {
		t.Fatal(err)
	}
	root, rootInfo, err := openDirectoryRoot(relativeRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	if err := pruneTreeAt(context.Background(), root, rootInfo, cutoff, excludedFileInfo(resumed)); err != nil {
		t.Fatal(err)
	}
	assertPruneTestExists(t, resumed)
	assertPruneTestMissing(t, expired)
}
