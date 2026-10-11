package openai

import (
	"encoding/json"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

func TestBuildBodyComposesChatRequest(t *testing.T) {
	effort := "high"
	cacheKey := "session-1"
	reasoningField := "reasoning_content"
	ctx := provider.Context{
		SystemPrompt: "Be concise.",
		Items:        []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("hello")}},
		Tools: []provider.ToolDefinition{{
			Name:        "read",
			Description: "Read a file.",
			Params:      []provider.ToolParam{{Name: "path", Type: "string", Required: true}},
		}},
		Effort:     &effort,
		ImageInput: 1,
	}
	body, err := BuildBody(ctx, "openrouter", "model-1", BodyOptions{
		CacheMarkers:    true,
		CacheTTL:        "1h",
		SessionCacheKey: &cacheKey,
		ReasoningField:  &reasoningField,
		RequestCost:     true,
		ExtraBody:       json.RawMessage(`{"stream_options":{"keep":true,"include_usage":false},"extra":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	if body["model"] != "model-1" || body["stream"] != true || body["prompt_cache_key"] != cacheKey {
		t.Fatalf("unexpected base request fields: %#v", body)
	}
	streamOptions := body["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != false || streamOptions["keep"] != true {
		t.Fatalf("extra_body did not recursively override stream_options: %#v", streamOptions)
	}
	if body["extra"] != float64(1) {
		t.Fatalf("extra_body field missing: %#v", body["extra"])
	}
	if body["usage"].(map[string]any)["include"] != true || body["reasoning_effort"] != effort {
		t.Fatalf("usage or reasoning options missing: %#v", body)
	}
	tools := body["tools"].([]map[string]any)
	toolFunction := tools[0]["function"].(map[string]any)
	if toolFunction["name"] != "read" || toolFunction["description"] != "Read a file." {
		t.Fatalf("tool definition malformed: %#v", toolFunction)
	}
	parameters := toolFunction["parameters"].(map[string]any)
	if parameters["type"] != "object" || len(parameters["required"].([]string)) != 1 {
		t.Fatalf("tool schema malformed: %#v", parameters)
	}
	messages := body["messages"].([]map[string]any)
	if len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %#v", messages)
	}
	if messages[0]["role"] != "system" || cacheControl(messages[0]) == nil {
		t.Fatalf("system cache breakpoint missing: %#v", messages[0])
	}
	userParts, ok := messages[1]["content"].([]any)
	if messages[1]["role"] != "user" || !ok || userParts[0].(map[string]any)["text"] != "hello" || cacheControl(messages[1]) == nil {
		t.Fatalf("user message malformed: %#v", messages[1])
	}
}

func TestBuildMessagesGroupsAssistantReasoningAndToolCalls(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("think"), Provider: conversation.String("llama.cpp"), Model: conversation.String("m1")},
		{Kind: conversation.KindToolCall, CallID: conversation.String("call-1"), ToolName: conversation.String("read"), Arguments: conversation.String(`{"path":"x"}`)},
		{Kind: conversation.KindAssistant, Text: conversation.String("Done.")},
		{Kind: conversation.KindToolResult, CallID: conversation.String("call-1"), Output: conversation.String("file contents")},
	}
	reasoningField := "reasoning_content"
	messages := BuildMessages(provider.Context{Items: items, ImageInput: 1}, "llamacpp", "m1", BodyOptions{ReasoningField: &reasoningField})
	if len(messages) != 2 {
		t.Fatalf("expected assistant and tool messages, got %#v", messages)
	}
	assistant := messages[0]
	if assistant["role"] != "assistant" || assistant["content"] != "Done." || assistant["reasoning_content"] != "think" {
		t.Fatalf("assistant message malformed: %#v", assistant)
	}
	calls := assistant["tool_calls"].([]map[string]any)
	function := calls[0]["function"].(map[string]any)
	if calls[0]["id"] != "call-1" || function["name"] != "read" || function["arguments"] != `{"path":"x"}` {
		t.Fatalf("tool call malformed: %#v", calls[0])
	}
	if messages[1]["role"] != "tool" || messages[1]["content"] != "file contents" {
		t.Fatalf("tool result malformed: %#v", messages[1])
	}
}

func TestBuildMessagesSendsRequiredReasoningFieldOnAssistantMessages(t *testing.T) {
	field := "reasoning_content"
	cases := []struct {
		name  string
		items []conversation.Item
		check func(*testing.T, map[string]any)
	}{
		{
			name:  "text",
			items: []conversation.Item{{Kind: conversation.KindAssistant, Text: conversation.String("answer")}},
			check: func(t *testing.T, message map[string]any) {
				if message["content"] != "answer" || message[field] != "" {
					t.Fatalf("assistant text replay = %#v", message)
				}
			},
		},
		{
			name: "tool call",
			items: []conversation.Item{{Kind: conversation.KindToolCall, CallID: conversation.String("call-1"),
				ToolName: conversation.String("read"), Arguments: conversation.String("{}")}},
			check: func(t *testing.T, message map[string]any) {
				if message[field] != "" || len(message["tool_calls"].([]map[string]any)) != 1 {
					t.Fatalf("tool-call replay = %#v", message)
				}
			},
		},
		{
			name: "typed reasoning",
			items: []conversation.Item{{Kind: conversation.KindReasoning, ReasoningJSON: conversation.String(`[{"type":"reasoning","text":"think"}]`),
				Provider: conversation.String("deepseek"), Model: conversation.String("m1")}},
			check: func(t *testing.T, message map[string]any) {
				if message[field] != "" || len(message["reasoning_details"].([]json.RawMessage)) != 1 {
					t.Fatalf("typed reasoning replay = %#v", message)
				}
			},
		},
		{
			name: "plain reasoning",
			items: []conversation.Item{{Kind: conversation.KindReasoning, ReasoningText: conversation.String("think"),
				Provider: conversation.String("deepseek"), Model: conversation.String("m1")}},
			check: func(t *testing.T, message map[string]any) {
				if message[field] != "think" {
					t.Fatalf("plain reasoning replay = %#v", message)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			messages := BuildMessages(provider.Context{Items: test.items}, "deepseek", "m1", BodyOptions{
				ReasoningField: &field, ReasoningRequired: true,
			})
			if len(messages) != 1 {
				t.Fatalf("assistant messages = %#v", messages)
			}
			test.check(t, messages[0])
		})
	}
	messages := BuildMessages(provider.Context{Items: []conversation.Item{{Kind: conversation.KindAssistant}}}, "deepseek", "m1", BodyOptions{
		ReasoningField: &field, ReasoningRequired: true,
	})
	if len(messages) != 0 {
		t.Fatalf("required reasoning added an otherwise empty assistant message: %#v", messages)
	}
}

func TestBuildMessagesUsesNullContentForReasoningOnlyAssistant(t *testing.T) {
	reasoningField := "reasoning"
	items := []conversation.Item{{
		Kind: conversation.KindReasoning, ReasoningText: conversation.String("think"),
		Provider: conversation.String("ollama"), Model: conversation.String("qwen3"),
	}}
	messages := BuildMessages(provider.Context{Items: items}, "ollama", "qwen3", BodyOptions{ReasoningField: &reasoningField})
	if len(messages) != 1 || messages[0]["content"] != nil || messages[0]["reasoning"] != "think" {
		t.Fatalf("reasoning-only replay should use null content and preserve reasoning: %#v", messages)
	}
}

func TestBuildMessagesDoesNotInferReasoningFieldFromTranscript(t *testing.T) {
	items := []conversation.Item{{
		Kind: conversation.KindReasoning, ReasoningText: conversation.String("think"), ReasoningField: "reasoning_content",
		Provider: conversation.String("gateway"), Model: conversation.String("m1"),
	}}
	messages := BuildMessages(provider.Context{Items: items}, "gateway", "m1", BodyOptions{})
	if len(messages) != 0 {
		t.Fatalf("transcript member name should not enable reasoning replay: %#v", messages)
	}

	configured := "reasoning"
	messages = BuildMessages(provider.Context{Items: items}, "gateway", "m1", BodyOptions{ReasoningField: &configured})
	if len(messages) != 1 || messages[0]["reasoning"] != "think" || messages[0]["reasoning_content"] != nil {
		t.Fatalf("configured reasoning field was not used for replay: %#v", messages)
	}
}

func TestBuildMessagesIgnoresRecordedReasoningFields(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("first"), ReasoningField: "role",
			Provider: conversation.String("gateway"), Model: conversation.String("m1")},
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("second"), ReasoningField: "reasoning_content",
			Provider: conversation.String("gateway"), Model: conversation.String("m1")},
	}
	configured := "reasoning_content"
	messages := BuildMessages(provider.Context{Items: items}, "gateway", "m1", BodyOptions{ReasoningField: &configured})
	if len(messages) != 1 || messages[0]["reasoning_content"] != "first\nsecond" {
		t.Fatalf("configured field should replay reasoning despite recorded fields: %#v", messages)
	}
}

func TestApplyCacheBreakpointsSkipsReasoningOnlyAssistant(t *testing.T) {
	reasoningField := "reasoning"
	items := []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("think about this")},
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("thinking"), Provider: conversation.String("ollama"), Model: conversation.String("qwen3")},
	}
	body, err := BuildBody(provider.Context{Items: items}, "ollama", "qwen3", BodyOptions{
		CacheMarkers: true, CacheTTL: "5m", ReasoningField: &reasoningField,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]map[string]any)
	if len(messages) != 2 || messages[1]["content"] != nil {
		t.Fatalf("reasoning-only assistant message missing: %#v", messages)
	}
	if cacheControl(messages[1]) != nil || cacheControl(messages[0]) == nil {
		t.Fatalf("cache breakpoint should skip empty assistant content and mark prior user content: %#v", messages)
	}
}

func cacheControl(message map[string]any) map[string]any {
	parts, ok := message["content"].([]any)
	if !ok || len(parts) == 0 {
		return nil
	}
	part, _ := parts[len(parts)-1].(map[string]any)
	control, _ := part["cache_control"].(map[string]any)
	return control
}
