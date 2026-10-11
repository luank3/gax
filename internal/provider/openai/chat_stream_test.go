package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/transport"
)

func TestStreamRetriesTransientStatusThenParsesSSE(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	var events []conversation.Event
	response, err := Stream(context.Background(), httpClient, server.URL, []string{"Content-Type: application/json"},
		[]byte(`{"model":"m1"}`), StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 2}}, nil,
		func(event conversation.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || response.StatusCode != http.StatusOK {
		t.Fatalf("expected one retry and a successful response, attempts=%d response=%#v", attempts, response)
	}
	if len(events) != 3 || events[0].Kind != conversation.EventRetry || events[0].HTTPStatus != http.StatusServiceUnavailable ||
		events[1].Kind != conversation.EventTextDelta || events[1].Text != "hello" || events[2].Kind != conversation.EventDone {
		t.Fatalf("unexpected stream events: %#v", events)
	}
	if strings.Contains(events[0].ErrorMessage, "busy") {
		t.Fatalf("retry event should not expose an intermediate response error: %#v", events[0])
	}
}

func TestStreamCapturesUsageSentAfterFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":6}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	var events []conversation.Event
	_, err := Stream(context.Background(), httpClient, server.URL, nil, []byte(`{"model":"m1"}`),
		StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 1}}, nil,
		func(event conversation.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != conversation.EventDone || events[0].Usage == nil ||
		events[0].Usage.InputTokens == nil || *events[0].Usage.InputTokens != 12 ||
		events[0].Usage.OutputTokens == nil || *events[0].Usage.OutputTokens != 6 {
		t.Fatalf("usage sent after finish_reason was not retained: %#v", events)
	}
}

func TestChatEventsExtractReasoningTextWithoutRecordingMemberName(t *testing.T) {
	tests := []struct {
		name, delta, text string
	}{
		{name: "reasoning_content", delta: `"reasoning_content":"think"`, text: "think"},
		{name: "reasoning has priority", delta: `"reasoning":"primary","reasoning_content":"secondary"`, text: "primary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []conversation.Event
			parser := NewChatEvents(func(event conversation.Event) bool {
				events = append(events, event)
				return true
			}, "", false)
			parser.Feed(`{"choices":[{"delta":{` + test.delta + `}}]}`)
			if len(events) != 1 || events[0].Kind != conversation.EventReasoningDelta || events[0].Text != test.text {
				t.Fatalf("Chat Completions reasoning text was not extracted: %#v", events)
			}
		})
	}
}
