package openai

import (
	"testing"

	"github.com/luank3/gax/internal/conversation"
)

func TestResponsesEventsTranslateToolAndTerminalUsage(t *testing.T) {
	var events []conversation.Event
	parser := NewResponsesEvents(func(event conversation.Event) { events = append(events, event) })
	parser.Feed(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`)
	parser.Feed(`{"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"bash"}}`)
	parser.Feed(`{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{}"}`)
	parser.Feed(`{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"bash","arguments":"{}"}}`)
	parser.Feed(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}}`)

	if len(events) != 4 || events[0].Kind != conversation.EventToolCallStart || events[1].Kind != conversation.EventToolCallDelta ||
		events[2].Kind != conversation.EventToolCallEnd || events[3].Kind != conversation.EventDone {
		t.Fatalf("unexpected translated events: %#v", events)
	}
	if events[0].ID == nil || *events[0].ID != "call_1" || events[0].Name != "bash" || events[1].Delta != "{}" {
		t.Fatalf("tool call identity or arguments changed: %#v", events[:3])
	}
	if !parser.Complete() || events[3].StopReason == nil || *events[3].StopReason != "completed" ||
		events[3].Response == nil || events[3].Response.ID == nil || *events[3].Response.ID != "resp_1" ||
		events[3].Response.Model == nil || *events[3].Response.Model != "gpt-5" {
		t.Fatalf("response metadata missing: %#v", events[3])
	}
	if events[3].Usage == nil || events[3].Usage.InputTokens == nil || *events[3].Usage.InputTokens != 10 ||
		events[3].Usage.CachedTokens == nil || *events[3].Usage.CachedTokens != 3 {
		t.Fatalf("terminal usage missing: %#v", events[3].Usage)
	}
}

func TestResponsesEventsSeparatesReasoningPartsAndFinalizesOnce(t *testing.T) {
	var events []conversation.Event
	parser := NewResponsesEvents(func(event conversation.Event) { events = append(events, event) })
	parser.Feed(`{"type":"response.reasoning_summary_text.delta","item_id":"r1","summary_index":0,"delta":"a"}`)
	parser.Feed(`{"type":"response.reasoning_summary_text.delta","item_id":"r1","summary_index":1,"delta":"b"}`)
	parser.Feed(`{"type":"response.output_text.delta","delta":"answer"}`)
	parser.Finalize()
	parser.Finalize()
	if len(events) != 5 || events[0].Kind != conversation.EventReasoningDelta || events[1].Text != "  \n" ||
		events[2].Kind != conversation.EventReasoningDelta || events[3].Kind != conversation.EventTextDelta ||
		events[4].Kind != conversation.EventError {
		t.Fatalf("reasoning boundaries or finalization changed: %#v", events)
	}
	if events[3].Text != "answer" || events[4].ErrorMessage != "stream ended before completion" || !parser.Complete() {
		t.Fatalf("unexpected parser terminal state: %#v", events)
	}
}
