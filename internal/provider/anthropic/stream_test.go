package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/transport"
)

func TestStreamRetriesThenTranslatesMessagesEvents(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-x\",\"usage\":{\"input_tokens\":8}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	var events []conversation.Event
	response, err := Stream(context.Background(), httpClient, server.URL, nil, []byte(`{}`),
		StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 2}}, nil,
		func(event conversation.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || attempts != 2 || len(events) != 3 ||
		events[0].Kind != conversation.EventRetry || events[1].Kind != conversation.EventTextDelta ||
		events[1].Text != "hello" || events[2].Kind != conversation.EventDone {
		t.Fatalf("unexpected retry or stream events: response=%#v attempts=%d events=%#v", response, attempts, events)
	}
	if events[2].Usage == nil || events[2].Usage.InputTokens == nil || *events[2].Usage.InputTokens != 8 ||
		events[2].Usage.OutputTokens == nil || *events[2].Usage.OutputTokens != 2 {
		t.Fatalf("terminal usage missing: %#v", events[2].Usage)
	}
}
