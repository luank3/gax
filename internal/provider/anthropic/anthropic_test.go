package anthropic_test

import (
	"context"
	"encoding/json"
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

func TestAnthropicGroupsToolCallsAndThinking(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := anthropic.NewClient(anthropic.Config{
		ProviderID: "anthropic", BaseURL: srv.URL, APIKey: "k",
		HTTP: transport.NewClient(transport.RetryPolicy{}, nil),
	})
	_ = c.Stream(context.Background(), provider.StreamRequest{
		Items: []core.Item{
			{Kind: core.KindUser, Text: "go", Images: []core.ImageAttachment{{MediaType: "image/png", Data: "AAAA"}}},
			{Kind: core.KindReasoning, ReasoningText: "plan", ReasoningJSON: "sig123"},
			{Kind: core.KindToolCall, CallID: "c1", ToolName: "read", Arguments: `{}`},
			{Kind: core.KindToolCall, CallID: "c2", ToolName: "bash", Arguments: `{}`},
			{Kind: core.KindToolResult, CallID: "c1", Output: "r1"},
			{Kind: core.KindToolResult, CallID: "c2", Output: "r2"},
		},
		Options: provider.StreamOptions{Model: "claude-sonnet-4-6", Effort: "high"},
	}, func(provider.StreamEvent) {})

	msgs := seen["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 grouped messages (user, assistant, user), got %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 {
		t.Fatalf("expected thinking + 2 tool_use blocks grouped in assistant, got %d", len(asst))
	}
	if _, ok := seen["thinking"]; !ok {
		t.Fatal("expected thinking config when effort is set")
	}
}
