//go:build linux

package osinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReleaseNameDoesNotBlockOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() { result <- ReleaseName(path) }()
	select {
	case got := <-result:
		if got != "" {
			t.Fatalf("FIFO release name = %q, want empty", got)
		}
	case <-time.After(100 * time.Millisecond):
		writer, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			_ = writer.Close()
		}
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("ReleaseName stayed blocked after FIFO cleanup")
		}
		t.Fatal("ReleaseName blocked while opening a FIFO")
	}
}

func TestDescriptionDoesNotDependOnPATHForKernelIdentity(t *testing.T) {
	_, wantRelease, ok := kernelIdentity()
	if !ok || wantRelease == "" {
		t.Fatal("uname(2) did not return a kernel release")
	}
	t.Setenv("PATH", t.TempDir())
	if got := Description(); !strings.Contains(got, fmt.Sprintf("Linux %s", wantRelease)) {
		t.Fatalf("Description() = %q, want the kernel release %q without PATH lookup", got, wantRelease)
	}
}
