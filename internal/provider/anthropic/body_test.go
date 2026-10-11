package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

func TestBuildBodyGroupsMessagesAndThinkingOptions(t *testing.T) {
	effort := "minimal"
	ctx := provider.Context{
		SystemPrompt: "be brief",
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("hi")},
			{Kind: conversation.KindReasoning, ReasoningJSON: conversation.String(`{"type":"thinking","thinking":"cot","signature":""}`), Provider: conversation.String("anthropic"), Model: conversation.String("m1")},
			{Kind: conversation.KindAssistant, Text: conversation.String("ok")},
			{Kind: conversation.KindToolCall, CallID: conversation.String("t1"), ToolName: conversation.String("bash"), Arguments: conversation.String(`{"cmd":"ls"}`)},
			{Kind: conversation.KindToolResult, CallID: conversation.String("t1"), Output: conversation.String("done")},
		},
		Tools:      []provider.ToolDefinition{{Name: "bash", Description: "run a command"}},
		Effort:     &effort,
		ImageInput: 1,
	}
	body, err := BuildBody(ctx, "anthropic", "m1", BodyOptions{
		CacheMarkers: true, CacheTTL: "1h", MaxTokens: 100, ThinkingMode: ThinkingAdaptive,
		ShowReasoning: false, ExtraBody: json.RawMessage(`{"vendor":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "m1" || body["max_tokens"] != 100 || body["stream"] != true || body["vendor"] != true {
		t.Fatalf("request fields missing: %#v", body)
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["display"] != "omitted" || body["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("adaptive thinking shape incorrect: %#v", body)
	}
	system := body["system"].([]map[string]any)
	if system[0]["text"] != "be brief" || system[0]["cache_control"].(map[string]any)["ttl"] != "1h" {
		t.Fatalf("system cache marker missing: %#v", system)
	}
	messages := body["messages"].([]map[string]any)
	if len(messages) != 3 || messages[0]["role"] != "user" || messages[1]["role"] != "assistant" || messages[2]["role"] != "user" {
		t.Fatalf("message grouping changed: %#v", messages)
	}
	assistantBlocks := messages[1]["content"].([]any)
	if len(assistantBlocks) != 3 || assistantBlocks[0].(map[string]any)["text"] != "cot" || assistantBlocks[1].(map[string]any)["text"] != "ok" || assistantBlocks[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant blocks malformed: %#v", assistantBlocks)
	}
	tool := body["tools"].([]map[string]any)[0]
	if tool["name"] != "bash" || tool["cache_control"] == nil || tool["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("tool schema or cache marker missing: %#v", tool)
	}
	lastContent := messages[2]["content"].([]any)
	lastBlock := lastContent[0].(map[string]any)
	if lastBlock["type"] != "tool_result" || lastBlock["cache_control"] == nil {
		t.Fatalf("tool result or conversation breakpoint missing: %#v", lastContent)
	}
}

func TestBuildMessagesDropsReasoningFromAnotherModel(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindReasoning, ReasoningJSON: conversation.String(`{"type":"thinking","thinking":"private","signature":"S"}`), Provider: conversation.String("anthropic"), Model: conversation.String("old")},
		{Kind: conversation.KindAssistant, Text: conversation.String("answer")},
	}
	messages := BuildMessages(items, "anthropic", "new", false, 0)
	blocks := messages[0]["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "answer" {
		t.Fatalf("stale reasoning replayed: %#v", blocks)
	}
}
