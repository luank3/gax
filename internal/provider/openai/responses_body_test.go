package openai

import (
	"encoding/json"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

func TestBuildResponsesBodyUsesFlatToolsAndEncryptedReasoning(t *testing.T) {
	effort := "medium"
	cacheKey := "sess-2"
	ctx := provider.Context{
		SystemPrompt: "be brief",
		Items: []conversation.Item{
			{Kind: conversation.KindReasoning, ReasoningJSON: conversation.String(`{"type":"reasoning","encrypted_content":"abc"}`), Provider: conversation.String("openai"), Model: conversation.String("gpt-5")},
			{Kind: conversation.KindUser, Text: conversation.String("hi")},
		},
		Tools:      []provider.ToolDefinition{{Name: "bash", Description: "run a command"}},
		Effort:     &effort,
		ImageInput: -1,
	}
	body, err := BuildResponsesBody(ctx, "openai", "gpt-5", BodyOptions{SessionCacheKey: &cacheKey})
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-5" || body["stream"] != true || body["store"] != false || body["instructions"] != "be brief" || body["prompt_cache_key"] != cacheKey {
		t.Fatalf("unexpected request fields: %#v", body)
	}
	if body["include"].([]string)[0] != "reasoning.encrypted_content" {
		t.Fatalf("encrypted reasoning not requested: %#v", body["include"])
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != effort || reasoning["summary"] != "auto" {
		t.Fatalf("unexpected reasoning options: %#v", reasoning)
	}
	input := body["input"].([]any)
	if len(input) != 2 || input[0].(map[string]any)["type"] != "reasoning" || input[1].(map[string]any)["role"] != "user" {
		t.Fatalf("unexpected input sequence: %#v", input)
	}
	tool := body["tools"].([]map[string]any)[0]
	if tool["type"] != "function" || tool["name"] != "bash" || tool["function"] != nil {
		t.Fatalf("Responses tool declaration must be flat: %#v", tool)
	}
	if body["tool_choice"] != "auto" || body["parallel_tool_calls"] != true {
		t.Fatalf("tool-call options missing: %#v", body)
	}
}

func TestBuildResponsesInputConvertsToolResultImages(t *testing.T) {
	items := []conversation.Item{{
		Kind: conversation.KindToolResult, CallID: conversation.String("c1"), Output: conversation.String("note"),
		Images: []conversation.Image{{MIME: "image/png", Data: "QUJD", Width: 4, Height: 2}},
	}}
	input := BuildResponsesInputItems(items, "openai", "m1", 1)
	callOutput := input[0].(map[string]any)
	output := callOutput["output"].([]map[string]any)
	if callOutput["type"] != "function_call_output" || output[0]["type"] != "input_text" || output[1]["type"] != "input_image" || output[1]["image_url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("image result malformed: %#v", callOutput)
	}

	input = BuildResponsesInputItems(items, "openai", "m1", 0)
	placeholder := input[0].(map[string]any)["output"].([]map[string]any)[1]["text"].(string)
	if placeholder == "" || placeholder[:8] != "[image: " {
		t.Fatalf("missing image placeholder: %q", placeholder)
	}
}

func TestBuildResponsesBodyMergesExtraBody(t *testing.T) {
	body, err := BuildResponsesBody(provider.Context{}, "openai", "m1", BodyOptions{ExtraBody: json.RawMessage(`{"stream":false,"vendor":{"x":1}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if body["stream"] != false || body["vendor"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("extra_body was not applied: %#v", body)
	}
}
