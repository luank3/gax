package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/anthropic"
	"github.com/luank3/gax/internal/transport"
)

func TestAnthropicStreamTextAndToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu1\",\"name\":\"read\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n")
	}))
	defer srv.Close()

	c := anthropic.NewClient(anthropic.Config{
		ProviderID: "anthropic",
		BaseURL:    srv.URL,
		APIKey:     "k",
		HTTP:       transport.NewClient(transport.RetryPolicy{}, nil),
	})
	var gotCall string
	var gotArgs string
	var gotInput int64
	err := c.Stream(context.Background(), provider.StreamRequest{
		Items:   []core.Item{{Kind: core.KindUser, Text: "read a.go"}},
		Options: provider.StreamOptions{Model: "claude-sonnet-4-6"},
	}, func(ev provider.StreamEvent) {
		if ev.Kind == provider.EventToolCallDone {
			gotCall = ev.ToolName
			gotArgs = ev.Arguments
		}
		if ev.Kind == provider.EventUsage && ev.Usage != nil {
			gotInput = ev.Usage.Input
		}
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotCall != "read" || gotArgs != `{"path":"a.go"}` || gotInput != 30 {
		t.Fatalf("call=%q args=%q input=%d", gotCall, gotArgs, gotInput)
	}
}
