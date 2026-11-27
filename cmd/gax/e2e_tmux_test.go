package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTmuxInteractiveTUIE2E(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gax-e2e")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	home := filepath.Join(tmp, "home")
	work := filepath.Join(tmp, "work")
	_ = os.MkdirAll(home, 0o755)
	_ = os.MkdirAll(work, 0o755)

	sessName := fmt.Sprintf("gax-e2e-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessName).Run()
	})

	cmdStr := fmt.Sprintf(
		"env HOME=%q XDG_CONFIG_HOME=%q XDG_STATE_HOME=%q XDG_CACHE_HOME=%q %q --provider mock",
		home, filepath.Join(home, ".config"), filepath.Join(home, ".state"), filepath.Join(home, ".cache"), bin,
	)
	start := exec.Command("tmux", "new-session", "-d", "-s", sessName, "-x", "100", "-y", "30", "-c", work, cmdStr)
	if out, err := start.CombinedOutput(); err != nil {
		t.Skipf("tmux new-session unavailable in this environment: %v (%s)", err, out)
	}

	waitForPane := func(substr string) string {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sessName).CombinedOutput()
			if err == nil && strings.Contains(string(out), substr) {
				return string(out)
			}
			time.Sleep(80 * time.Millisecond)
		}
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", sessName).CombinedOutput()
		t.Fatalf("timed out waiting for %q in tmux pane:\n%s", substr, out)
		return ""
	}

	waitForPane("Type /help for commands")

	// Test Tab completion for /eff -> /effort, then set effort high.
	_ = exec.Command("tmux", "send-keys", "-t", sessName, "/eff", "Tab").Run()
	waitForPane("/effort")
	_ = exec.Command("tmux", "send-keys", "-t", sessName, " high", "Enter").Run()
	waitForPane("effort: high")

	// Test an interactive mock turn.
	_ = exec.Command("tmux", "send-keys", "-t", sessName, "hello from tmux", "Enter").Run()
	waitForPane("turns · in")

	// Quit cleanly with Ctrl-D.
	_ = exec.Command("tmux", "send-keys", "-t", sessName, "C-d").Run()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("tmux", "has-session", "-t", sessName).Run(); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("tmux session did not exit after Ctrl-D")
}
