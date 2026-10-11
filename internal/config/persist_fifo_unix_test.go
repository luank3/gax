//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPersistPresetRejectsPromptFIFOWithoutBlocking(t *testing.T) {
	if fifoPath := os.Getenv("GAX_TEST_PROMPT_FIFO"); fifoPath != "" {
		store := New(nil)
		if err := store.PersistPreset("review", Preset{Provider: "mock", SystemPrompt: stringPtr("@" + fifoPath)}); err == nil {
			t.Fatal("PersistPreset unexpectedly accepted a FIFO prompt")
		}
		return
	}

	fifoPath := filepath.Join(t.TempDir(), "prompt.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistPresetRejectsPromptFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "GAX_TEST_PROMPT_FIFO="+fifoPath)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("prompt FIFO validation blocked until timeout: %s", output)
	}
	if err != nil {
		t.Fatalf("child test failed: %v\n%s", err, output)
	}
}
