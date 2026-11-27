package wiretrace_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/wiretrace"
)

func TestTracerRedactsHeadersAndSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	tr, err := wiretrace.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tr.RegisterSecret("sk-secret-123456")
	tr.RegisterSecret("env-header-token-xyz")

	hdr := http.Header{
		"Authorization":    {"Bearer sk-secret-123456"},
		"X-Api-Key":        {"secret"},
		"X-Custom-Gateway": {"token=env-header-token-xyz"},
		"Content-Type":     {"application/json"},
	}
	tr.TraceRequest("POST", "https://api.example.com/v1/chat", hdr, []byte(`{"key":"sk-secret-123456"}`))
	tr.TraceResponse(200, 125*time.Millisecond, nil)
	tr.TraceSSE("message", `{"echo":"env-header-token-xyz"}`, 150*time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"sk-secret-123456", "env-header-token-xyz"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("trace leaked secret %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in trace:\n%s", text)
	}
}
