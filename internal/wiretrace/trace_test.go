package wiretrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerRedactsCredentialHeadersAndRegisteredBodySecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	if err := os.WriteFile(path, []byte("stale trace"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := New()
	if err := logger.Configure(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	logger.RegisterSecret("PORTKEYSECRET")
	logger.Request("POST", "https://example.test/token", []string{
		"x-api-key: sk-ant-SECRETVALUE", "Authorization: Bearer BEARERSECRET", "API-Key: AZURESECRET",
		"X-Portkey: prefix PORTKEYSECRET embedded", "X-Unregistered: LITERALVALUE", "Content-Type: application/json",
		"anthropic-version: 2023-06-01",
	}, []byte(`{"refresh_token":"PORTKEYSECRET"}`))
	logger.ResponseStatus(400, `{"error":"bad token PORTKEYSECRET"}`)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, secret := range []string{"sk-ant-SECRETVALUE", "BEARERSECRET", "AZURESECRET", "PORTKEYSECRET", "stale trace"} {
		if strings.Contains(text, secret) {
			t.Errorf("trace leaked %q: %s", secret, text)
		}
	}
	for _, want := range []string{
		"x-api-key: <redacted>", "Authorization: <redacted>", "API-Key: <redacted>",
		"X-Portkey: <redacted>", "X-Unregistered: LITERALVALUE", "anthropic-version: 2023-06-01",
		`"refresh_token": "<redacted>"`, `"error": "bad token <redacted>"`, "## POST https://example.test/token",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("trace missing %q: %s", want, text)
		}
	}
}

func TestLoggerRedactsLongestOverlappingSecretAndFencesBackticks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	logger := New()
	if err := logger.Configure(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	logger.RegisterSecret("ROTATED")
	logger.RegisterSecret("ROTATEDLONGER")
	logger.Request("POST", "https://example.test", nil, []byte("not json ` ``` ROTATEDLONGER"))
	logger.SSEEvent("delta", `{"text":"hello"}`)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, "LONGER") || !strings.Contains(text, "````text") ||
		!strings.Contains(text, "### event: delta") || !strings.Contains(text, `"text": "hello"`) {
		t.Fatalf("trace redaction or fenced rendering failed: %s", text)
	}
	if !strings.Contains(text, "t+0.000s dt+0.000s") {
		t.Fatalf("trace did not anchor its first timestamp at zero: %s", text)
	}
}

func TestDisabledLoggerDoesNotCreateOrWriteAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "trace.md")
	logger := New()
	logger.RegisterSecret("secret")
	if logger.Enabled() {
		t.Fatal("new logger is enabled before configuration")
	}
	logger.Request("GET", "https://example.test", nil, nil)
	logger.ResponseStatus(200, "")
	logger.SSEEvent("message", "{}")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled logger created a file: %v", err)
	}
}
