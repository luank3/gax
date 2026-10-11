//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package catalog_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/luank3/gax/internal/catalog"
)

func TestLoadCacheRejectsFIFOWithoutBlocking(t *testing.T) {
	if fifoPath := os.Getenv("GAX_TEST_CATALOG_FIFO"); fifoPath != "" {
		if err := catalog.New(nil).LoadCache(fifoPath, 1024); err == nil {
			t.Fatal("LoadCache unexpectedly accepted a FIFO")
		}
		return
	}

	fifoPath := filepath.Join(t.TempDir(), "catalog.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoadCacheRejectsFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "GAX_TEST_CATALOG_FIFO="+fifoPath)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("catalog cache read blocked until timeout: %s", output)
	}
	if err != nil {
		t.Fatalf("child test failed: %v\n%s", err, output)
	}
}
