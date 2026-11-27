package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/openai"
	"github.com/luank3/gax/internal/transport"
)

func TestResponsesStreamAndLengthIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	}))
	defer srv.Close()

	c := openai.NewResponsesClient(openai.Config{
		ProviderID: "openai",
		BaseURL:    srv.URL,
		APIKey:     "k",
		HTTP:       transport.NewClient(transport.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond}, nil),
	})

	var gotText string
	var gotIncomplete bool
	err := c.Stream(context.Background(), provider.StreamRequest{
		Items:   []core.Item{{Kind: core.KindUser, Text: "hello"}},
		Options: provider.StreamOptions{Model: "gpt-5"},
	}, func(ev provider.StreamEvent) {
		if ev.Kind == provider.EventText {
			gotText += ev.Text
		}
		if ev.Kind == provider.EventDone {
			gotIncomplete = ev.IncompleteLength
		}
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotText != "hi" || !gotIncomplete {
		t.Fatalf("text=%q incomplete=%v", gotText, gotIncomplete)
	}
}

func TestChatReasoningRequiredAndOverflow(t *testing.T) {
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seenBody)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"This model's maximum context length is 4096 tokens"}}`)
	}))
	defer srv.Close()

	c := openai.NewChatClient(openai.Config{
		ProviderID: "deepseek",
		BaseURL:    srv.URL,
		APIKey:     "k",
		HTTP:       transport.NewClient(transport.RetryPolicy{}, nil),
	})
	err := c.Stream(context.Background(), provider.StreamRequest{
		Items: []core.Item{
			{Kind: core.KindUser, Text: "q"},
			{Kind: core.KindToolCall, CallID: "c1", ToolName: "bash", Arguments: "{}"},
			{Kind: core.KindToolResult, CallID: "c1", Output: "ok"},
		},
		Options: provider.StreamOptions{Model: "deepseek-chat", ReasoningRequired: true},
	}, func(provider.StreamEvent) {})

	var ov *provider.OverflowError
	if !errors.As(err, &ov) {
		t.Fatalf("expected OverflowError, got %v", err)
	}
	msgs, _ := seenBody["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if _, ok := asst["reasoning_content"]; !ok {
		t.Fatalf("expected reasoning_content on assistant message when reasoning_required=true: %+v", asst)
	}
}

func TestChatImageAndRequestCost(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := openai.NewChatClient(openai.Config{
		ProviderID: "openrouter", BaseURL: srv.URL, APIKey: "k",
		HTTP: transport.NewClient(transport.RetryPolicy{}, nil),
	})
	_ = c.Stream(context.Background(), provider.StreamRequest{
		Items:   []core.Item{{Kind: core.KindUser, Text: "see", Images: []core.ImageAttachment{{MediaType: "image/png", Data: "AAAA"}}}},
		Options: provider.StreamOptions{Model: "gpt-5", RequestCost: true},
	}, func(provider.StreamEvent) {})

	if _, ok := seen["usage"]; !ok {
		t.Fatal("expected usage.include when RequestCost=true")
	}
	msgs := seen["messages"].([]any)
	if _, ok := msgs[0].(map[string]any)["content"].([]any); !ok {
		t.Fatalf("expected multipart content array for image, got %T", msgs[0].(map[string]any)["content"])
	}
}
