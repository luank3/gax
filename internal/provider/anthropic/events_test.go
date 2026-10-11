package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/luank3/gax/internal/conversation"
)

func TestEventsAssembleThinkingAndToolCalls(t *testing.T) {
	var events []conversation.Event
	parser := NewEvents(func(event conversation.Event) { events = append(events, event) })
	parser.Feed(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	parser.Feed(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`)
	parser.Feed(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`)
	parser.Feed(`{"type":"content_block_stop","index":0}`)
	parser.Feed(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"bash","input":{}}}`)
	parser.Feed(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
	parser.Feed(`{"type":"content_block_stop","index":1}`)
	if len(events) != 6 || events[0].Kind != conversation.EventReasoningDelta || events[0].Text != "" ||
		events[1].Text != "reason" || events[2].Kind != conversation.EventReasoningItem ||
		events[3].Kind != conversation.EventToolCallStart || events[4].Kind != conversation.EventToolCallDelta ||
		events[5].Kind != conversation.EventToolCallEnd {
		t.Fatalf("unexpected Anthropic events: %#v", events)
	}
	var reasoning map[string]any
	if err := json.Unmarshal([]byte(*events[2].JSON), &reasoning); err != nil || reasoning["thinking"] != "reason" || reasoning["signature"] != "sig" {
		t.Fatalf("reasoning block failed to round-trip: %s err=%v", *events[2].JSON, err)
	}
}

func TestEventsMergeUsageAndRejectIncompleteStopReason(t *testing.T) {
	var events []conversation.Event
	parser := NewEvents(func(event conversation.Event) { events = append(events, event) })
	parser.Feed(`{"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":10,"output_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":7}}}}`)
	parser.Feed(`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":25}}`)
	parser.Feed(`{"type":"message_stop"}`)
	if len(events) != 1 || events[0].Kind != conversation.EventError || events[0].ErrorMessage == "" || !parser.Complete() {
		t.Fatalf("max_tokens must emit one terminal error: %#v", events)
	}
	usage := events[0].Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 150 || usage.CachedTokens == nil || *usage.CachedTokens != 40 ||
		usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 10 || usage.CacheWrite1HTokens == nil || *usage.CacheWrite1HTokens != 7 ||
		usage.OutputTokens == nil || *usage.OutputTokens != 25 {
		t.Fatalf("Anthropic usage fields were not merged: %#v", usage)
	}
	if events[0].Response == nil || events[0].Response.ID == nil || *events[0].Response.ID != "msg_1" ||
		events[0].Response.Model == nil || *events[0].Response.Model != "claude-x" {
		t.Fatalf("response identity missing: %#v", events[0].Response)
	}
	parser.Finalize()
	if len(events) != 1 {
		t.Fatalf("finalize emitted after terminal state: %#v", events)
	}
}
