package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildTestBinary(t *testing.T) string {
	t.Helper()
	exeOut, err := exec.Command("go", "env", "GOEXE").Output()
	if err != nil {
		t.Fatalf("go env GOEXE: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "gax"+strings.TrimSpace(string(exeOut)))
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestE2EOneshotPipedStdinAndJSONResume(t *testing.T) {
	bin := buildTestBinary(t)
	stateDir := t.TempDir()
	cfgDir := t.TempDir()
	workDir := t.TempDir()

	baseEnv := append(os.Environ(),
		"XDG_STATE_HOME="+stateDir,
		"XDG_CONFIG_HOME="+cfgDir,
		"GAX_PROVIDER=mock",
		"GAX_NO_SESSION=0",
	)

	// 1. -p with piped stdin alongside prompt arg
	cmd := exec.Command(bin, "-p", "review")
	cmd.Dir = workDir
	cmd.Env = baseEnv
	cmd.Stdin = strings.NewReader("diff --git a/x b/x")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oneshot failed: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review") || !strings.Contains(stdout.String(), "diff --git") {
		t.Fatalf("expected combined prompt+stdin echoed by mock, got %q", stdout.String())
	}

	// 2. --json mode emits session, items, and result with session_id
	cmd2 := exec.Command(bin, "--json", "hello json")
	cmd2.Dir = workDir
	cmd2.Env = baseEnv
	out2, err := cmd2.Output()
	if err != nil {
		t.Fatalf("--json failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out2)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected >=3 JSONL lines, got %d:\n%s", len(lines), out2)
	}
	var last struct {
		Type      string `json:"type"`
		Outcome   string `json:"outcome"`
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("parse result line: %v", err)
	}
	if last.Type != "result" || last.Outcome != "complete" || last.SessionID == "" || last.Text != "hello json" {
		t.Fatalf("unexpected result record: %+v", last)
	}

	// 3. --resume=ID -p continues the saved session
	cmd3 := exec.Command(bin, "--resume="+last.SessionID[:8], "-p", "follow up")
	cmd3.Dir = workDir
	cmd3.Env = baseEnv
	out3, err := cmd3.Output()
	if err != nil {
		t.Fatalf("--resume failed: %v", err)
	}
	if strings.TrimSpace(string(out3)) != "follow up" {
		t.Fatalf("unexpected resumed output: %q", out3)
	}
}
