//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLoadDefaultsIgnoresConfigFIFOWithoutBlocking(t *testing.T) {
	if home := os.Getenv("GAX_TEST_CODEX_SETTINGS_HOME"); home != "" {
		model, effort := LoadDefaults(home)
		if model != "" || effort != "" {
			t.Fatalf("LoadDefaults() = (%q, %q), want empty defaults", model, effort)
		}
		return
	}

	home := t.TempDir()
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(configPath, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoadDefaultsIgnoresConfigFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "GAX_TEST_CODEX_SETTINGS_HOME="+home)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Codex settings read blocked until timeout: %s", output)
	}
	if err != nil {
		t.Fatalf("child test failed: %v\n%s", err, output)
	}
}
