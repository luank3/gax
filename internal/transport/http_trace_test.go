package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/wiretrace"
)

func TestHTTPRequestsAndSSEEventsAreTraced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.md")
	logger := wiretrace.New()
	if err := logger.Configure(path); err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	logger.RegisterSecret("body-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/buffered":
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "body-secret") || request.Header.Get("X-Token") != "body-secret" {
				t.Errorf("request did not reach server intact: body=%q token=%q", body, request.Header.Get("X-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"rejected body-secret"}`)
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: delta\ndata: {\"text\":\"hello\"}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	client := NewClient()
	client.HTTP = server.Client()
	client.trace = logger

	body, status, err := client.Post(context.Background(), server.URL+"/buffered",
		[]string{"X-Token: body-secret"}, "application/json", []byte(`{"token":"body-secret"}`), time.Second, 1024, nil)
	if err != nil || status != http.StatusBadRequest || len(body) == 0 {
		t.Fatalf("buffered POST = body %q, status %d, err %v", body, status, err)
	}
	var received SSEEvent
	response, err := client.SSEPost(context.Background(), server.URL+"/stream", nil, []byte(`{"stream":true}`),
		time.Second, func(event SSEEvent) bool { received = event; return true }, nil)
	if err != nil || response.StatusCode != http.StatusOK || received.Name != "delta" || received.Data != `{"text":"hello"}` {
		t.Fatalf("SSE response/event = %+v / %+v, err %v", response, received, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, "body-secret") {
		t.Fatalf("trace leaked a request credential: %s", text)
	}
	for _, expected := range []string{
		"## POST " + server.URL + "/buffered", "X-Token: <redacted>", `"token": "<redacted>"`,
		"**HTTP 400**", `"error": "rejected <redacted>"`, "## POST " + server.URL + "/stream",
		"### event: delta", `"text": "hello"`, "**HTTP 200**",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("trace missing %q: %s", expected, text)
		}
	}
}
