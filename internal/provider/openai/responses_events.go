package openai

import (
	"encoding/json"

	"github.com/luank3/gax/internal/conversation"
)

type responsesToolCall struct {
	itemID       string
	callID       string
	sawArgsDelta bool
}

// ResponsesEvents translates OpenAI Responses SSE payloads into provider-independent events.
type ResponsesEvents struct {
	callback               func(conversation.Event)
	toolCalls              []responsesToolCall
	terminalEmitted        bool
	responseID             *string
	servedModel            *string
	reasoningItemID        *string
	reasoningPartIndex     int64
	reasoningPartIsContent bool
}

func NewResponsesEvents(callback func(conversation.Event)) *ResponsesEvents {
	return &ResponsesEvents{callback: callback}
}

func (parser *ResponsesEvents) Complete() bool { return parser.terminalEmitted }

func (parser *ResponsesEvents) Feed(data string) {
	if data == "" {
		return
	}
	if data == "[DONE]" {
		parser.handleCompleted(nil)
		return
	}
	root, ok := decodeObject([]byte(data))
	if !ok {
		return
	}
	typeName := jsonString(root["type"])
	if typeName == nil {
		return
	}
	parser.captureResponse(root)
	switch *typeName {
	case "response.output_item.added":
		parser.handleOutputItemAdded(root)
	case "response.output_item.done":
		parser.handleOutputItemDone(root)
	case "response.output_text.delta", "response.refusal.delta":
		if delta := jsonString(root["delta"]); delta != nil {
			parser.emit(conversation.Event{Kind: conversation.EventTextDelta, Text: *delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		parser.handleReasoningDelta(root)
	case "response.function_call_arguments.delta":
		parser.handleToolCallDelta(root)
	case "response.completed", "response.done":
		parser.handleCompleted(root)
	case "response.incomplete":
		response, _ := decodeObject(root["response"])
		details, _ := decodeObject(response["incomplete_details"])
		reason := jsonString(details["reason"])
		if reason == nil {
			reason = stringPointer("unknown")
		}
		parser.emitTerminalError("response incomplete: "+*reason, root)
	case "response.failed":
		response, _ := decodeObject(root["response"])
		errorObject, _ := decodeObject(response["error"])
		message := jsonString(errorObject["message"])
		if message == nil {
			message = stringPointer("response.failed")
		}
		parser.emitTerminalError(*message, root)
	case "error":
		message := jsonString(root["message"])
		if message == nil {
			message = jsonString(root["code"])
		}
		if message == nil {
			message = stringPointer("provider error")
		}
		parser.emitTerminalError(*message, root)
	}
}

func (parser *ResponsesEvents) Finalize() {
	parser.emitTerminalError("stream ended before completion", nil)
}

func (parser *ResponsesEvents) emit(event conversation.Event) {
	switch event.Kind {
	case conversation.EventTextDelta, conversation.EventToolCallStart, conversation.EventReasoningItem,
		conversation.EventDone, conversation.EventError:
		parser.reasoningItemID = nil
	}
	if parser.callback != nil {
		parser.callback(event)
	}
}

func (parser *ResponsesEvents) handleOutputItemAdded(root map[string]json.RawMessage) {
	item, _ := decodeObject(root["item"])
	if jsonString(item["type"]) == nil || *jsonString(item["type"]) != "function_call" {
		return
	}
	itemID := jsonString(item["id"])
	callID := jsonString(item["call_id"])
	name := jsonString(item["name"])
	if itemID == nil || callID == nil || name == nil {
		return
	}
	parser.toolCalls = append(parser.toolCalls, responsesToolCall{itemID: *itemID, callID: *callID})
	parser.emit(conversation.Event{
		Kind: conversation.EventToolCallStart,
		ID:   cloneString(callID),
		Name: *name,
	})
}

func (parser *ResponsesEvents) findToolCall(itemID *string) *responsesToolCall {
	if itemID == nil {
		return nil
	}
	for i := range parser.toolCalls {
		if parser.toolCalls[i].itemID == *itemID {
			return &parser.toolCalls[i]
		}
	}
	return nil
}

func (parser *ResponsesEvents) handleOutputItemDone(root map[string]json.RawMessage) {
	item, _ := decodeObject(root["item"])
	typeName := jsonString(item["type"])
	if typeName == nil {
		return
	}
	switch *typeName {
	case "function_call":
		itemID := jsonString(item["id"])
		call := parser.findToolCall(itemID)
		if call == nil {
			return
		}
		arguments := jsonString(item["arguments"])
		if !call.sawArgsDelta && arguments != nil && *arguments != "" {
			parser.emit(conversation.Event{
				Kind: conversation.EventToolCallDelta,
				ID:   stringPointer(call.callID), Delta: *arguments,
			})
		}
		parser.emit(conversation.Event{Kind: conversation.EventToolCallEnd, ID: stringPointer(call.callID)})
	case "reasoning":
		encrypted := item["encrypted_content"]
		if len(encrypted) == 0 || bytesEqualNull(encrypted) {
			return
		}
		replay := map[string]json.RawMessage{"type": json.RawMessage(`"reasoning"`)}
		if summary, ok := item["summary"]; ok {
			replay["summary"] = append(json.RawMessage(nil), summary...)
		} else {
			replay["summary"] = json.RawMessage("[]")
		}
		replay["encrypted_content"] = append(json.RawMessage(nil), encrypted...)
		encoded, err := json.Marshal(replay)
		if err != nil {
			return
		}
		value := string(encoded)
		parser.emit(conversation.Event{Kind: conversation.EventReasoningItem, JSON: &value})
	}
}

func (parser *ResponsesEvents) handleToolCallDelta(root map[string]json.RawMessage) {
	itemID := jsonString(root["item_id"])
	delta := jsonString(root["delta"])
	if itemID == nil || delta == nil || *delta == "" {
		return
	}
	call := parser.findToolCall(itemID)
	if call == nil {
		return
	}
	call.sawArgsDelta = true
	parser.emit(conversation.Event{Kind: conversation.EventToolCallDelta, ID: stringPointer(call.callID), Delta: *delta})
}

func (parser *ResponsesEvents) handleReasoningDelta(root map[string]json.RawMessage) {
	delta := jsonString(root["delta"])
	if delta == nil || *delta == "" {
		return
	}
	parser.emitReasoningPartBreak(root)
	parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta, Text: *delta})
}

func (parser *ResponsesEvents) emitReasoningPartBreak(root map[string]json.RawMessage) {
	itemID := jsonString(root["item_id"])
	index := jsonInt(root["summary_index"])
	isContent := false
	if index == nil {
		index = jsonInt(root["content_index"])
		isContent = true
	}
	if itemID == nil || index == nil {
		return
	}
	sameItem := parser.reasoningItemID != nil && *parser.reasoningItemID == *itemID
	partChanged := parser.reasoningItemID != nil && (!sameItem || *index != parser.reasoningPartIndex || isContent != parser.reasoningPartIsContent)
	if partChanged {
		parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta, Text: "  \n"})
	}
	if !sameItem {
		parser.reasoningItemID = cloneString(itemID)
	}
	parser.reasoningPartIndex = *index
	parser.reasoningPartIsContent = isContent
}

func (parser *ResponsesEvents) captureResponse(root map[string]json.RawMessage) {
	response, ok := decodeObject(root["response"])
	if !ok {
		return
	}
	parser.responseID = firstString(parser.responseID, response["id"])
	parser.servedModel = firstString(parser.servedModel, response["model"])
}

func (parser *ResponsesEvents) responseCopy() *conversation.StreamResponse {
	return &conversation.StreamResponse{ID: cloneString(parser.responseID), Model: cloneString(parser.servedModel)}
}

func (parser *ResponsesEvents) usageCopy() *conversation.Usage { return nil }

func (parser *ResponsesEvents) UsageSnapshot() *conversation.Usage { return nil }

func (parser *ResponsesEvents) ResponseSnapshot() *conversation.StreamResponse {
	return parser.responseCopy()
}

func responsesUsage(root map[string]json.RawMessage) *conversation.Usage {
	response, _ := decodeObject(root["response"])
	usageObject, _ := decodeObject(response["usage"])
	usage := &conversation.Usage{
		InputTokens:  jsonInt(usageObject["input_tokens"]),
		OutputTokens: jsonInt(usageObject["output_tokens"]),
	}
	details, _ := decodeObject(usageObject["input_tokens_details"])
	usage.CachedTokens = jsonInt(details["cached_tokens"])
	return usage
}

func (parser *ResponsesEvents) emitTerminalError(message string, root map[string]json.RawMessage) {
	if parser.terminalEmitted {
		return
	}
	parser.terminalEmitted = true
	parser.emit(conversation.Event{
		Kind: conversation.EventError, ErrorMessage: message, Usage: responsesUsage(root), Response: parser.responseCopy(),
	})
}

func (parser *ResponsesEvents) handleCompleted(root map[string]json.RawMessage) {
	if parser.terminalEmitted {
		return
	}
	parser.terminalEmitted = true
	reason := "completed"
	parser.emit(conversation.Event{
		Kind: conversation.EventDone, StopReason: &reason, Usage: responsesUsage(root), Response: parser.responseCopy(),
	})
}
