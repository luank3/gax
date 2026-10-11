//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadPickerLabelRejectsFIFOWithoutBlocking(t *testing.T) {
	if fifoPath := os.Getenv("GAX_TEST_SESSION_FIFO"); fifoPath != "" {
		if label := ReadPickerLabel(fifoPath, 80); label != (PickerLabel{}) {
			t.Fatalf("FIFO label = %+v, want empty label", label)
		}
		return
	}

	fifoPath := filepath.Join(t.TempDir(), "session.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadPickerLabelRejectsFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "GAX_TEST_SESSION_FIFO="+fifoPath)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("session label read blocked until timeout: %s", output)
	}
	if err != nil {
		t.Fatalf("child test failed: %v\n%s", err, output)
	}
}
