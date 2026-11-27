// Package wiretrace records redacted HTTP/SSE diagnostics to a trace file.
package wiretrace

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const redacted = "[REDACTED]"

// Tracer writes redacted HTTP requests, responses, and SSE events.
type Tracer struct {
	mu      sync.Mutex
	file    *os.File
	secrets []string
}

// Open creates or truncates path for trace output. If path is empty, returns nil.
func Open(path string) (*Tracer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open trace %q: %w", path, err)
	}
	return &Tracer{file: f}, nil
}

// Close closes the underlying trace file.
func (t *Tracer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

// RegisterSecret adds a secret string to be scrubbed from all trace output.
func (t *Tracer) RegisterSecret(secret string) {
	if t == nil || len(secret) < 4 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.secrets {
		if s == secret {
			return
		}
	}
	t.secrets = append(t.secrets, secret)
	// Sort longest first so partial prefixes don't mask longer tokens.
	sort.Slice(t.secrets, func(i, j int) bool {
		return len(t.secrets[i]) > len(t.secrets[j])
	})
}

// TraceRequest logs an outgoing HTTP request with auth headers and secrets redacted.
func (t *Tracer) TraceRequest(method, rawURL string, headers http.Header, body []byte) {
	if t == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s %s (%s)\n", method, t.redact(rawURL), time.Now().UTC().Format(time.RFC3339))
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := strings.Join(headers[k], ", ")
		if isAuthHeader(k) {
			val = redacted
		} else {
			val = t.redact(val)
		}
		fmt.Fprintf(&b, "- %s: %s\n", k, val)
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "```json\n%s\n```\n", t.redact(string(body)))
	}
	t.write(b.String())
}

// TraceResponse logs an HTTP response status or error with elapsed duration.
func (t *Tracer) TraceResponse(status int, elapsed time.Duration, err error) {
	if t == nil {
		return
	}
	if err != nil {
		t.write(fmt.Sprintf("<- error (%s): %s\n", elapsed.Round(time.Millisecond), t.redact(err.Error())))
		return
	}
	t.write(fmt.Sprintf("<- HTTP %d (%s)\n", status, elapsed.Round(time.Millisecond)))
}

// TraceSSE logs one SSE event with elapsed time.
func (t *Tracer) TraceSSE(event, data string, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.write(fmt.Sprintf("  [+%s] event=%q data=%s\n", elapsed.Round(time.Millisecond), event, t.redact(data)))
}

func (t *Tracer) write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		_, _ = t.file.WriteString(s)
	}
}

func (t *Tracer) redact(s string) string {
	t.mu.Lock()
	secrets := append([]string(nil), t.secrets...)
	t.mu.Unlock()
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, redacted)
	}
	return s
}

func isAuthHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "x-api-key", "x-subscription-token", "proxy-authorization", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}
