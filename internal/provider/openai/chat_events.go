package openai

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/luank3/gax/internal/conversation"
)

type ChatEvents struct {
	callback            func(conversation.Event) bool
	toolCalls           []*chatToolCall
	reasoningDetails    []map[string]json.RawMessage
	reasoningSeen       bool
	finishReceived      bool
	finishReason        *string
	finishError         string
	finishTransient     bool
	usage               conversation.Usage
	responseID          *string
	servedModel         *string
	route               *string
	terminalEmitted     bool
	lengthHint          string
	cacheWriteIsOneHour bool
}

type chatToolCall struct {
	index                int64
	id                   *string
	name                 *string
	argumentsBeforeStart strings.Builder
	started              bool
	finished             bool
}

func NewChatEvents(callback func(conversation.Event) bool, lengthHint string,
	cacheWriteIsOneHour bool) *ChatEvents {
	return &ChatEvents{
		callback:            callback,
		lengthHint:          lengthHint,
		cacheWriteIsOneHour: cacheWriteIsOneHour,
	}
}

func (parser *ChatEvents) Feed(data string) {
	if parser.terminalEmitted || data == "" {
		return
	}
	if data == "[DONE]" {
		parser.handleDone()
		return
	}
	root, ok := decodeObject([]byte(data))
	if !ok {
		return
	}
	if errorObject, ok := decodeObject(root["error"]); ok {
		message := jsonString(errorObject["message"])
		text := "provider error"
		if message != nil {
			text = *message
		}
		parser.terminalEmitted = true
		parser.emit(conversation.Event{
			Kind:         conversation.EventError,
			ErrorMessage: text,
			Usage:        parser.usageCopy(),
			Response:     parser.responseCopy(),
		})
		return
	}

	parser.captureResponse(root)
	parser.captureUsage(root)
	parser.handleProgress(root)
	if choices, ok := decodeArray(root["choices"]); ok && len(choices) > 0 {
		if choice, ok := decodeObject(choices[0]); ok {
			parser.handleChoice(choice)
		}
	}
}

func (parser *ChatEvents) Complete() bool {
	return parser.terminalEmitted || parser.finishReceived && !parser.finishTransient
}

func (parser *ChatEvents) Finalize() {
	if parser.terminalEmitted {
		return
	}
	parser.terminalEmitted = true
	if parser.finishReceived {
		parser.emitTerminal()
		return
	}
	parser.emit(conversation.Event{
		Kind:         conversation.EventError,
		ErrorMessage: "stream ended before completion",
		Usage:        parser.usageCopy(),
		Response:     parser.responseCopy(),
	})
}

func (parser *ChatEvents) captureResponse(root map[string]json.RawMessage) {
	parser.responseID = firstString(parser.responseID, root["id"])
	parser.servedModel = firstString(parser.servedModel, root["model"])
	parser.route = firstString(parser.route, root["provider"])
}

func (parser *ChatEvents) captureUsage(root map[string]json.RawMessage) {
	usage, ok := decodeObject(root["usage"])
	if !ok {
		return
	}
	parser.usage.InputTokens = setInteger(parser.usage.InputTokens, usage["prompt_tokens"])
	parser.usage.OutputTokens = setInteger(parser.usage.OutputTokens, usage["completion_tokens"])
	if details, ok := decodeObject(usage["prompt_tokens_details"]); ok {
		parser.usage.CachedTokens = setInteger(parser.usage.CachedTokens, details["cached_tokens"])
		parser.usage.CacheWriteTokens = setInteger(parser.usage.CacheWriteTokens, details["cache_write_tokens"])
		if parser.cacheWriteIsOneHour && parser.usage.CacheWriteTokens != nil {
			value := *parser.usage.CacheWriteTokens
			parser.usage.CacheWrite1HTokens = &value
		}
	}
	if cost := jsonNumber(usage["cost"]); cost != nil && *cost >= 0 {
		parser.usage.Cost = cost
	}
}

func (parser *ChatEvents) handleProgress(root map[string]json.RawMessage) {
	progress, ok := decodeObject(root["prompt_progress"])
	if !ok {
		return
	}
	parser.emit(conversation.Event{
		Kind:      conversation.EventProgress,
		Processed: integerValue(progress["processed"]),
		Total:     integerValue(progress["total"]),
		Cache:     integerValue(progress["cache"]),
	})
}

func (parser *ChatEvents) handleChoice(choice map[string]json.RawMessage) {
	if delta, ok := decodeObject(choice["delta"]); ok {
		parser.collectReasoningDetails(delta)
		parser.handleReasoningDelta(delta)

		content := jsonString(delta["content"])
		toolCalls, toolCallsIsArray := decodeArray(delta["tool_calls"])
		if content != nil && *content != "" || toolCallsIsArray {
			parser.flushReasoningDetails()
		}
		if content != nil && *content != "" {
			parser.emit(conversation.Event{Kind: conversation.EventTextDelta, Text: *content})
		}
		if toolCallsIsArray {
			for _, rawCall := range toolCalls {
				if call, ok := decodeObject(rawCall); ok {
					parser.handleToolCallDelta(call)
				}
			}
		}
	}
	finishReason := jsonString(choice["finish_reason"])
	nativeReason := jsonString(choice["native_finish_reason"])
	if finishReason != nil || isUpstreamError(nativeReason) {
		parser.handleFinishReason(finishReason, nativeReason)
	}
}

func (parser *ChatEvents) handleReasoningDelta(delta map[string]json.RawMessage) {
	for _, field := range []string{"reasoning", "reasoning_content"} {
		text := jsonString(delta[field])
		if text == nil {
			continue
		}
		if *text == "" {
			return
		}
		parser.emit(conversation.Event{Kind: conversation.EventReasoningDelta, Text: *text})
		return
	}
}

func (parser *ChatEvents) collectReasoningDetails(delta map[string]json.RawMessage) {
	details, ok := decodeArray(delta["reasoning_details"])
	if !ok {
		return
	}
	parser.reasoningSeen = true
	for _, rawDetail := range details {
		detail, ok := decodeObject(rawDetail)
		if !ok {
			continue
		}
		if count := len(parser.reasoningDetails); count > 0 && joinReasoningText(parser.reasoningDetails[count-1], detail) {
			continue
		}
		parser.reasoningDetails = append(parser.reasoningDetails, detail)
	}
}

func joinReasoningText(block, detail map[string]json.RawMessage) bool {
	blockType := jsonString(block["type"])
	detailType := jsonString(detail["type"])
	if blockType == nil || detailType == nil || *blockType != "reasoning.text" || *detailType != "reasoning.text" {
		return false
	}
	if tail := jsonString(detail["text"]); tail != nil && *tail != "" {
		head := jsonString(block["text"])
		prefix := ""
		if head != nil {
			prefix = *head
		}
		encoded, _ := json.Marshal(prefix + *tail)
		block["text"] = encoded
	}
	for _, field := range []string{"signature", "format"} {
		if hasMember(detail[field]) && !hasMember(block[field]) {
			block[field] = append(json.RawMessage(nil), detail[field]...)
		}
	}
	return true
}

func hasMember(raw json.RawMessage) bool {
	if len(raw) == 0 || bytesEqualNull(raw) {
		return false
	}
	if text := jsonString(raw); text != nil {
		return *text != ""
	}
	return true
}

func (parser *ChatEvents) flushReasoningDetails() {
	if !parser.reasoningSeen {
		return
	}
	parser.reasoningSeen = false
	details := parser.reasoningDetails
	parser.reasoningDetails = nil
	if len(details) == 0 {
		return
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return
	}
	text := string(encoded)
	parser.emit(conversation.Event{Kind: conversation.EventReasoningItem, JSON: &text})
}

func (parser *ChatEvents) handleToolCallDelta(delta map[string]json.RawMessage) {
	index := integerValue(delta["index"])
	call := parser.getToolCall(index)
	if id := jsonString(delta["id"]); id != nil && call.id == nil {
		call.id = id
	}
	function, _ := decodeObject(delta["function"])
	if name := jsonString(function["name"]); name != nil && call.name == nil {
		call.name = name
	}
	parser.startToolCall(call)
	arguments := jsonString(function["arguments"])
	if arguments == nil || *arguments == "" {
		return
	}
	if !call.started {
		call.argumentsBeforeStart.WriteString(*arguments)
		return
	}
	parser.emit(conversation.Event{
		Kind:  conversation.EventToolCallDelta,
		ID:    cloneString(call.id),
		Delta: *arguments,
	})
}

func (parser *ChatEvents) getToolCall(index int64) *chatToolCall {
	for _, call := range parser.toolCalls {
		if call.index == index {
			return call
		}
	}
	call := &chatToolCall{index: index}
	parser.toolCalls = append(parser.toolCalls, call)
	return call
}

func (parser *ChatEvents) startToolCall(call *chatToolCall) {
	if call.started || call.name == nil {
		return
	}
	if call.id == nil {
		id := fmt.Sprintf("call_%d", call.index)
		call.id = &id
	}
	parser.emit(conversation.Event{
		Kind: conversation.EventToolCallStart,
		ID:   cloneString(call.id),
		Name: *call.name,
	})
	call.started = true
	if call.argumentsBeforeStart.Len() > 0 {
		parser.emit(conversation.Event{
			Kind:  conversation.EventToolCallDelta,
			ID:    cloneString(call.id),
			Delta: call.argumentsBeforeStart.String(),
		})
		call.argumentsBeforeStart.Reset()
	}
}

func (parser *ChatEvents) finishToolCalls() {
	for _, call := range parser.toolCalls {
		if !call.started || call.finished {
			continue
		}
		parser.emit(conversation.Event{Kind: conversation.EventToolCallEnd, ID: cloneString(call.id)})
		call.finished = true
	}
}

func (parser *ChatEvents) handleFinishReason(reason, nativeReason *string) {
	if parser.terminalEmitted || parser.finishReceived {
		return
	}
	parser.flushReasoningDetails()
	parser.finishToolCalls()
	parser.finishReceived = true
	upstreamError := isUpstreamError(nativeReason) || isUpstreamError(reason)
	truncated := reason != nil && (*reason == "length" || *reason == "content_filter")
	if !truncated && !upstreamError {
		if reason == nil {
			parser.finishReason = stringPointer("stop")
		} else {
			parser.finishReason = cloneString(reason)
		}
		return
	}
	if upstreamError {
		if isUpstreamError(nativeReason) {
			parser.finishError = "upstream error: " + *nativeReason
		} else {
			parser.finishError = "upstream error: " + *reason
		}
		parser.finishTransient = true
		return
	}
	if *reason == "length" && parser.lengthHint != "" {
		parser.finishError = "response incomplete: length — " + parser.lengthHint
	} else {
		parser.finishError = "response incomplete: " + *reason
	}
}

func (parser *ChatEvents) handleDone() {
	if parser.terminalEmitted || parser.finishTransient {
		return
	}
	parser.flushReasoningDetails()
	parser.finishToolCalls()
	parser.terminalEmitted = true
	parser.emitTerminal()
}

func (parser *ChatEvents) emitTerminal() {
	reason := cloneString(parser.finishReason)
	if reason == nil {
		reason = stringPointer("stop")
	}
	if parser.finishError != "" {
		parser.emit(conversation.Event{
			Kind:         conversation.EventError,
			ErrorMessage: parser.finishError,
			Usage:        parser.usageCopy(),
			Response:     parser.responseCopy(),
		})
		return
	}
	parser.emit(conversation.Event{
		Kind:       conversation.EventDone,
		StopReason: reason,
		Usage:      parser.usageCopy(),
		Response:   parser.responseCopy(),
	})
}

func (parser *ChatEvents) emit(event conversation.Event) {
	if parser.callback != nil {
		parser.callback(event)
	}
}

func (parser *ChatEvents) usageCopy() *conversation.Usage {
	usage := parser.usage
	usage.InputTokens = cloneInt64(usage.InputTokens)
	usage.OutputTokens = cloneInt64(usage.OutputTokens)
	usage.CachedTokens = cloneInt64(usage.CachedTokens)
	usage.CacheWriteTokens = cloneInt64(usage.CacheWriteTokens)
	usage.CacheWrite1HTokens = cloneInt64(usage.CacheWrite1HTokens)
	usage.Cost = cloneFloat64(usage.Cost)
	return &usage
}

func (parser *ChatEvents) responseCopy() *conversation.StreamResponse {
	return &conversation.StreamResponse{
		ID:    cloneString(parser.responseID),
		Model: cloneString(parser.servedModel),
		Route: cloneString(parser.route),
	}
}

func (parser *ChatEvents) UsageSnapshot() *conversation.Usage { return parser.usageCopy() }

func (parser *ChatEvents) ResponseSnapshot() *conversation.StreamResponse {
	return parser.responseCopy()
}

func firstString(current *string, raw json.RawMessage) *string {
	if current != nil {
		return current
	}
	value := jsonString(raw)
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func decodeArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) != nil {
		return nil, false
	}
	return array, true
}

func jsonString(raw json.RawMessage) *string {
	if len(raw) == 0 || raw[0] != '"' {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func jsonInt(raw json.RawMessage) *int64 {
	if len(raw) == 0 || raw[0] == '"' || strings.ContainsAny(string(raw), ".eE") {
		return nil
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func integerValue(raw json.RawMessage) int64 {
	value := jsonInt(raw)
	if value == nil {
		return 0
	}
	return *value
}

func setInteger(current *int64, raw json.RawMessage) *int64 {
	if value := jsonInt(raw); value != nil {
		return value
	}
	return current
}

func jsonNumber(raw json.RawMessage) *float64 {
	if len(raw) == 0 || !(raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9') {
		return nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func bytesEqualNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func isUpstreamError(reason *string) bool {
	return reason != nil && (*reason == "network_error" || *reason == "error")
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func stringPointer(value string) *string { return &value }

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
