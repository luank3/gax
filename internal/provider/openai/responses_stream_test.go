package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/transport"
)

func TestResponsesStreamRoutesSSEThroughResponsesParser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.refusal.delta\",\"delta\":\"No\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer server.Close()
	httpClient := transport.NewClient()
	httpClient.HTTP = server.Client()
	var events []conversation.Event
	response, err := StreamResponses(context.Background(), httpClient, server.URL, nil, []byte(`{}`),
		StreamOptions{RetryPolicy: transport.RetryPolicy{MaxAttempts: 1}}, nil,
		func(event conversation.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(events) != 2 || events[0].Kind != conversation.EventTextDelta ||
		events[0].Text != "No" || events[1].Kind != conversation.EventDone {
		t.Fatalf("Responses stream was not translated: response=%#v events=%#v", response, events)
	}
}
