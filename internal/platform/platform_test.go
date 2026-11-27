package platform_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/luank3/gax/internal/platform"
)

func TestResolveDirsRespectsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "st"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "ch"))

	dirs := platform.ResolveDirs()
	if dirs.Config != filepath.Join(tmp, "cfg", "gax") {
		t.Fatalf("unexpected Config dir: %q", dirs.Config)
	}
	if dirs.State != filepath.Join(tmp, "st", "gax") {
		t.Fatalf("unexpected State dir: %q", dirs.State)
	}
	if dirs.Cache != filepath.Join(tmp, "ch", "gax") {
		t.Fatalf("unexpected Cache dir: %q", dirs.Cache)
	}
}

func TestProjectBucketCanonicalizesSymlink(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-proj")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmp, "link-proj")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	b1 := platform.ProjectBucket(realDir)
	b2 := platform.ProjectBucket(linkDir)
	if b1 != b2 {
		t.Fatalf("expected identical bucket for symlink, got %q vs %q", b1, b2)
	}
}

func TestLockFile(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	l, err := platform.LockFile(lockPath)
	if err != nil {
		t.Fatalf("LockFile: %v", err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestResolveShell(t *testing.T) {
	spec, err := platform.ResolveShell("")
	if err != nil {
		t.Fatalf("ResolveShell: %v", err)
	}
	if spec.Path == "" || len(spec.Command) < 2 {
		t.Fatalf("unexpected ShellSpec: %+v", spec)
	}
}

func TestProcessGroupKillTree(t *testing.T) {
	spec, err := platform.ResolveShell("")
	if err != nil {
		t.Fatalf("ResolveShell: %v", err)
	}
	args := append(append([]string{}, spec.Command[1:]...), "sleep 30")
	cmd := exec.Command(spec.Command[0], args...)
	pg, err := platform.StartCommand(cmd)
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	defer pg.Close()

	done := make(chan error, 1)
	go func() {
		done <- pg.Wait()
	}()

	if err := pg.KillTree(); err != nil {
		t.Fatalf("KillTree: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process group did not exit after KillTree")
	}
}
