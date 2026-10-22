//go:build !windows

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/luank3/gax/internal/session"
)

func TestRunOneShotSIGUSR1PausesAndKeepsSessionResumable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"finished response\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	stateHome := t.TempDir()
	cwd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunOneShotSignalChild$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GAX_SIGNAL_CHILD=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	released := false
	defer func() {
		if !released {
			close(release)
		}
		if !finished && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("one-shot child did not start its provider request")
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	// Go delivers signal.Notify events asynchronously; keep the request blocked until the pause
	// signal can be observed so this test exercises the in-flight pause path deterministically.
	time.Sleep(100 * time.Millisecond)
	released = true
	close(release)
	err := cmd.Wait()
	finished = true
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("paused one-shot exit = %v, want exit status 1; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	var result map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["type"] == "result" {
			result = record
		}
	}
	if result == nil || result["outcome"] != "paused" || !strings.Contains(stderr.String(), "paused at a turn boundary") {
		t.Fatalf("SIGUSR1 result = %#v, stderr=%q, stdout=%q", result, stderr.String(), stdout.String())
	}
	entries, err := session.List(cwd, 30)
	if err != nil || len(entries) != 1 || entries[0].ID == nil {
		t.Fatalf("paused session was not retained for resume: entries=%+v err=%v", entries, err)
	}
}

func TestRunOneShotSecondSIGINTEscalatesAndKillsBashGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arguments, _ := json.Marshal(map[string]string{
			"command": "printf '%s' \"$$\" > \"$GAX_TASK_MARKER\"; while :; do printf x >> \"$GAX_HEARTBEAT\"; sleep 0.05; done",
		})
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call_long", "type": "function",
					"function": map[string]any{"name": "bash", "arguments": string(arguments)},
				}}},
				"finish_reason": "tool_calls",
			}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer server.Close()
	cwd := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GAX_OPENAI_BASE_URL", server.URL)
	t.Setenv("GAX_OPENAI_API_KEY", "test-key")
	t.Setenv("GAX_NO_SESSION", "off")
	t.Setenv("GAX_TASK_MARKER", marker)
	t.Setenv("GAX_HEARTBEAT", heartbeat)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunOneShotSignalChild$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GAX_SIGNAL_CHILD=escalate")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if data, err := os.ReadFile(marker); err == nil {
			if pid, parseErr := strconv.Atoi(string(data)); parseErr == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	}()
	waitForTestFile(t, marker, 5*time.Second)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		finished = true
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("second SIGINT exit = %v, want signal termination; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		status, ok := exitError.Sys().(syscall.WaitStatus)
		if !ok || status.Signal() != syscall.SIGINT {
			if exitError.ExitCode() != 128+int(syscall.SIGINT) {
				t.Fatalf("second SIGINT status = %#v, want SIGINT termination or its 130 shell status; stderr=%q", exitError.Sys(), stderr.String())
			}
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("second SIGINT did not force termination promptly; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	waitForStableFile(t, heartbeat, 200*time.Millisecond)
}
