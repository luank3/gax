package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/luank3/gax/internal/conversation"
)

type contentKind uint8

const (
	contentOther contentKind = iota
	contentText
	contentThinking
	contentRedactedThinking
	contentToolUse
)

type contentBlock struct {
	index       int64
	kind        contentKind
	toolCallID  string
	toolName    string
	toolStarted bool
	thinking    strings.Builder
	signature   strings.Builder
	redacted    *string
}

// Events translates Anthropic Messages SSE payloads into provider-independent stream events.
type Events struct {
	callback        func(conversation.Event)
	blocks          []contentBlock
	usage           conversation.Usage
	responseID      *string
	servedModel     *string
	stopReason      *string
	terminalEmitted bool
}

func NewEvents(callback func(conversation.Event)) *Events { return &Events{callback: callback} }

func (parser *Events) Complete() bool { return parser.terminalEmitted }

func (parser *Events) Feed(data string) {
	if parser.terminalEmitted || data == "" {
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
	switch *typeName {
	case "message_start":
		message, _ := decodeObject(root["message"])
		parser.responseID = firstString(parser.responseID, message["id"])
		parser.servedModel = firstString(parser.servedModel, message["model"])
		parser.captureUsage(message["usage"])
	case "content_block_start":
		parser.handleContentBlockStart(root)
	case "content_block_delta":
		parser.handleContentBlockDelta(root)
	case "content_block_stop":
		parser.handleContentBlockStop(root)
	case "message_delta":
		delta, _ := decodeObject(root["delta"])
		if reason := jsonString(delta["stop_reason"]); reason != nil {
			parser.stopReason = cloneString(reason)
		}
		parser.captureUsage(root["usage"])
	case "message_stop":
		parser.handleMessageStop()
	case "error":
		errorObject, _ := decodeObject(root["error"])
		message := jsonString(errorObject["message"])
		if message == nil {
			message = stringPointer("provider error")
		}
		parser.emitTerminalError(*message)
	}
}

func (parser *Events) Finalize() {
	if !parser.terminalEmitted {
		parser.emitTerminalError("stream ended before completion")
	}
}

func (parser *Events) emit(event conversation.Event) {
	if parser.callback != nil {
		parser.callback(event)
	}
}

func (parser *Events) findBlock(index int64) *contentBlock {
	for i := range parser.blocks {
		if parser.blocks[i].index == index {
			return &parser.blocks[i]
		}
	}
	return nil
}

func (parser *Events) getBlock(index int64) *contentBlock {
	if block := parser.findBlock(index); block != nil {
		return block
	}
	parser.blocks = append(parser.blocks, contentBlock{index: index})
	return &parser.blocks[len(parser.blocks)-1]
}

func (parser *Events) handleContentBlockStart(root map[string]json.RawMessage) {
	index := integerValue(root["index"])
	content, _ := decodeObject(root["content_block"])
	typeName := jsonString(content["type"])
	if typeName == nil {
		return
	}
	block := parser.getBlock(index)
	switch *typeName {
	case "text":
		block.kind = contentText
	case "thinking":
		block.kind = contentThinking
		parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta})
	case "redacted_thinking":
		block.kind = contentRedactedThinking
		block.redacted = cloneString(jsonString(content["data"]))
		parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta})
	case "tool_use":
		block.kind = contentToolUse
		block.toolCallID = stringValue(jsonString(content["id"]))
		block.toolName = stringValue(jsonString(content["name"]))
		parser.emit(conversation.Event{
			Kind: conversation.EventToolCallStart, ID: stringPointer(block.toolCallID), Name: block.toolName,
		})
		block.toolStarted = true
	default:
		block.kind = contentOther
	}
}

func (parser *Events) handleContentBlockDelta(root map[string]json.RawMessage) {
	index := integerValue(root["index"])
	delta, _ := decodeObject(root["delta"])
	typeName := jsonString(delta["type"])
	if typeName == nil {
		return
	}
	block := parser.findBlock(index)
	switch *typeName {
	case "text_delta":
		if text := jsonString(delta["text"]); text != nil && *text != "" {
			parser.emit(conversation.Event{Kind: conversation.EventTextDelta, Text: *text})
		}
	case "thinking_delta":
		if text := jsonString(delta["thinking"]); text != nil && *text != "" {
			if block != nil {
				block.thinking.WriteString(*text)
			}
			parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta, Text: *text})
		}
	case "signature_delta":
		if signature := jsonString(delta["signature"]); signature != nil && *signature != "" && block != nil {
			block.signature.WriteString(*signature)
		}
	case "input_json_delta":
		partial := jsonString(delta["partial_json"])
		if partial != nil && *partial != "" && block != nil && block.toolStarted {
			parser.emit(conversation.Event{
				Kind: conversation.EventToolCallDelta, ID: stringPointer(block.toolCallID), Delta: *partial,
			})
		}
	}
}

func (parser *Events) handleContentBlockStop(root map[string]json.RawMessage) {
	block := parser.findBlock(integerValue(root["index"]))
	if block == nil {
		return
	}
	switch block.kind {
	case contentToolUse:
		if block.toolStarted {
			parser.emit(conversation.Event{Kind: conversation.EventToolCallEnd, ID: stringPointer(block.toolCallID)})
		}
	case contentThinking, contentRedactedThinking:
		parser.emitReasoningItem(block)
	}
}

func (parser *Events) emitReasoningItem(block *contentBlock) {
	var item map[string]any
	if block.kind == contentRedactedThinking {
		if block.redacted == nil {
			return
		}
		item = map[string]any{"type": "redacted_thinking", "data": *block.redacted}
	} else {
		thinking := block.thinking.String()
		signature := block.signature.String()
		if thinking == "" && signature == "" {
			return
		}
		item = map[string]any{"type": "thinking", "thinking": thinking, "signature": signature}
	}
	encoded, err := json.Marshal(item)
	if err == nil {
		value := string(encoded)
		parser.emit(conversation.Event{Kind: conversation.EventReasoningItem, JSON: &value})
	}
}

func (parser *Events) captureUsage(raw json.RawMessage) {
	usage, ok := decodeObject(raw)
	if !ok {
		return
	}
	input := jsonInt(usage["input_tokens"])
	cacheRead := jsonInt(usage["cache_read_input_tokens"])
	cacheWrite := jsonInt(usage["cache_creation_input_tokens"])
	if input != nil {
		value := *input
		if cacheRead != nil && *cacheRead > 0 {
			value += *cacheRead
		}
		if cacheWrite != nil && *cacheWrite > 0 {
			value += *cacheWrite
		}
		parser.usage.InputTokens = &value
	}
	if cacheRead != nil && *cacheRead >= 0 {
		parser.usage.CachedTokens = cacheRead
	}
	if cacheWrite != nil && *cacheWrite >= 0 {
		parser.usage.CacheWriteTokens = cacheWrite
	}
	cacheCreation, _ := decodeObject(usage["cache_creation"])
	if oneHour := jsonInt(cacheCreation["ephemeral_1h_input_tokens"]); oneHour != nil {
		parser.usage.CacheWrite1HTokens = oneHour
	}
	if output := jsonInt(usage["output_tokens"]); output != nil && *output >= 0 {
		parser.usage.OutputTokens = output
	}
}

func (parser *Events) handleMessageStop() {
	if parser.terminalEmitted {
		return
	}
	if parser.stopReason != nil && *parser.stopReason == "max_tokens" {
		parser.emitTerminalError("response incomplete: max_tokens — raise the provider's max_tokens or lower the effort level")
		return
	}
	if parser.stopReason != nil && *parser.stopReason == "pause_turn" {
		parser.emitTerminalError("response paused before completion (pause_turn)")
		return
	}
	reason := "end_turn"
	if parser.stopReason != nil {
		reason = *parser.stopReason
	}
	parser.terminalEmitted = true
	parser.emit(conversation.Event{
		Kind: conversation.EventDone, StopReason: &reason, Usage: parser.usageCopy(), Response: parser.responseCopy(),
	})
}

func (parser *Events) emitTerminalError(message string) {
	if parser.terminalEmitted {
		return
	}
	parser.terminalEmitted = true
	parser.emit(conversation.Event{
		Kind: conversation.EventError, ErrorMessage: message, Usage: parser.usageCopy(), Response: parser.responseCopy(),
	})
}

func (parser *Events) usageCopy() *conversation.Usage {
	usage := parser.usage
	usage.InputTokens = cloneInt64(usage.InputTokens)
	usage.OutputTokens = cloneInt64(usage.OutputTokens)
	usage.CachedTokens = cloneInt64(usage.CachedTokens)
	usage.CacheWriteTokens = cloneInt64(usage.CacheWriteTokens)
	usage.CacheWrite1HTokens = cloneInt64(usage.CacheWrite1HTokens)
	return &usage
}

func (parser *Events) responseCopy() *conversation.StreamResponse {
	return &conversation.StreamResponse{ID: cloneString(parser.responseID), Model: cloneString(parser.servedModel)}
}

func (parser *Events) UsageSnapshot() *conversation.Usage { return parser.usageCopy() }

func (parser *Events) ResponseSnapshot() *conversation.StreamResponse { return parser.responseCopy() }
