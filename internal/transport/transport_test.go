package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luank3/gax/internal/transport"
)

func TestParseSSE(t *testing.T) {
	raw := ": comment\r\n" +
		"event: delta\r\n" +
		"data: hello\r\n" +
		"data: world\r\n\r\n" +
		"data: [DONE]\n"
	var events []transport.SSEEvent
	err := transport.ParseSSE(strings.NewReader(raw), func(ev transport.SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	if events[0].Event != "delta" || events[0].Data != "hello\nworld" {
		t.Fatalf("unexpected event 0: %+v", events[0])
	}
	if events[1].Data != "[DONE]" {
		t.Fatalf("unexpected event 1: %+v", events[1])
	}
}

func TestDoStreamRetriesTransientStatus(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer srv.Close()

	client := transport.NewClient(transport.RetryPolicy{
		MaxRetries: 3,
		BaseDelay:  time.Millisecond,
	}, nil)

	var retries int
	resp, err := client.DoStream(context.Background(), "POST", srv.URL, nil, []byte(`{}`), 0, func(int, time.Duration, string) {
		retries++
	})
	if err != nil {
		t.Fatalf("DoStream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || retries != 2 {
		t.Fatalf("status=%d retries=%d", resp.StatusCode, retries)
	}
}

func TestBackoffDelayHonorsRetryAfter(t *testing.T) {
	d := transport.BackoffDelay(0, time.Second, "2")
	if d != 2*time.Second {
		t.Fatalf("expected 2s, got %s", d)
	}
}
