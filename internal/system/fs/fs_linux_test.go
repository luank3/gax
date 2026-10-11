//go:build linux

package fs

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenRegularRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		file, err := OpenRegular(path)
		if file != nil {
			_ = file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("OpenRegular accepted a FIFO")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OpenRegular blocked while opening a FIFO")
	}
}
