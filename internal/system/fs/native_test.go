package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	systemfs "github.com/luank3/gax/internal/system/fs"
)

func TestNativeFileLockAndTouch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	first, err := systemfs.OpenNoFollow(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := systemfs.OpenNoFollow(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := systemfs.Lock(first, false, false); err != nil {
		t.Fatal(err)
	}
	if err := systemfs.Lock(second, true, true); err == nil {
		t.Fatal("Lock(exclusive) succeeded while another handle holds a shared lock")
	}
	if _, err := first.WriteString("session\n"); err != nil {
		t.Fatal(err)
	}
	if err := systemfs.Unlock(first); err != nil {
		t.Fatal(err)
	}
	if err := systemfs.Lock(second, true, true); err != nil {
		t.Fatalf("Lock(after unlock) = %v, want nil", err)
	}
	if err := systemfs.Unlock(second); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	if err := systemfs.TouchFile(first, now); err != nil {
		t.Fatal(err)
	}
	info, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !systemfs.HasLinks(first, info) || info.ModTime().Sub(now).Abs() > time.Second {
		t.Fatalf("TouchFile() modtime = %v, want %v and linked file", info.ModTime(), now)
	}
}

func TestOpenNoFollowRejectsSymlinkBeforeTruncation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	file, err := systemfs.OpenNoFollow(link, os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		file.Close()
		t.Error("OpenNoFollow(symlink) succeeded, want rejection")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Errorf("OpenNoFollow(symlink) changed target to %q, want keep", data)
	}
}
