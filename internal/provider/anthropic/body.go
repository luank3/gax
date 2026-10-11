package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

type ThinkingMode uint8

const (
	ThinkingAdaptive ThinkingMode = iota
	ThinkingBudget
	ThinkingOff
)

type BodyOptions struct {
	CacheMarkers        bool
	CacheTTL            string
	MaxTokens           int
	ThinkingMode        ThinkingMode
	ThinkingBudget      int
	ShowReasoning       bool
	AllowEmptySignature bool
	ExtraBody           json.RawMessage
}

func ParseThinkingMode(value string) (ThinkingMode, bool) {
	switch {
	case strings.EqualFold(value, "adaptive"):
		return ThinkingAdaptive, true
	case strings.EqualFold(value, "budget"):
		return ThinkingBudget, true
	case strings.EqualFold(value, "off"):
		return ThinkingOff, true
	default:
		return ThinkingAdaptive, false
	}
}

func EffortExpressible(effort string) bool { return effort != "none" && effort != "minimal" }

func BuildMessages(items []conversation.Item, providerID, model string, allowEmptySignature bool,
	imageInput int) []map[string]any {
	messages := make([]map[string]any, 0, len(items))
	for index := 0; index < len(items); {
		item := items[index]
		switch item.Kind {
		case conversation.KindUser:
			messages = append(messages, map[string]any{
				"role": "user", "content": []any{textBlock(itemText(item.Text))},
			})
			index++
		case conversation.KindAssistant, conversation.KindToolCall, conversation.KindReasoning:
			index = appendAssistantMessage(&messages, items, index, providerID, model, allowEmptySignature)
		case conversation.KindToolResult:
			index = appendToolResults(&messages, items, index, imageInput)
		default:
			index++
		}
	}
	return messages
}

func appendAssistantMessage(messages *[]map[string]any, items []conversation.Item, index int,
	providerID, model string, allowEmptySignature bool) int {
	content := make([]any, 0)
	for index < len(items) {
		item := items[index]
		if item.Kind != conversation.KindAssistant && item.Kind != conversation.KindToolCall && item.Kind != conversation.KindReasoning {
			break
		}
		index++
		switch item.Kind {
		case conversation.KindAssistant:
			if item.Text != nil && *item.Text != "" {
				content = append(content, textBlock(*item.Text))
			}
		case conversation.KindReasoning:
			if item.Provider != nil && item.Model != nil && providerIdentity(*item.Provider) == providerIdentity(providerID) && *item.Model == model {
				if block, ok := reasoningBlock(item.ReasoningJSON, allowEmptySignature); ok {
					content = append(content, block)
				}
			}
		case conversation.KindToolCall:
			content = append(content, toolUseBlock(item))
		}
	}
	if len(content) > 0 {
		*messages = append(*messages, map[string]any{"role": "assistant", "content": content})
	}
	return index
}

func reasoningBlock(raw *string, allowEmptySignature bool) (any, bool) {
	if raw == nil || !json.Valid([]byte(*raw)) {
		return nil, false
	}
	var block any
	if json.Unmarshal([]byte(*raw), &block) != nil {
		return nil, false
	}
	object, ok := block.(map[string]any)
	if ok && object["type"] == "thinking" && !allowEmptySignature {
		signature, isString := object["signature"].(string)
		if !isString || signature == "" {
			thinking, _ := object["thinking"].(string)
			if thinking == "" {
				return nil, false
			}
			return textBlock(thinking), true
		}
	}
	if !ok {
		return json.RawMessage(*raw), true
	}
	return object, true
}

func toolUseBlock(item conversation.Item) map[string]any {
	var input map[string]any
	if item.Arguments == nil || json.Unmarshal([]byte(*item.Arguments), &input) != nil || input == nil {
		input = make(map[string]any)
	}
	return map[string]any{
		"type": "tool_use", "id": itemText(item.CallID), "name": itemText(item.ToolName), "input": input,
	}
}

func appendToolResults(messages *[]map[string]any, items []conversation.Item, index, imageInput int) int {
	content := make([]any, 0)
	for index < len(items) && items[index].Kind == conversation.KindToolResult {
		content = append(content, toolResultBlock(items[index], imageInput))
		index++
	}
	*messages = append(*messages, map[string]any{"role": "user", "content": content})
	return index
}

func toolResultBlock(item conversation.Item, imageInput int) map[string]any {
	if len(item.Images) == 0 {
		return map[string]any{"type": "tool_result", "tool_use_id": itemText(item.CallID), "content": itemText(item.Output)}
	}
	content := make([]any, 0, len(item.Images)+1)
	if item.Output != nil && *item.Output != "" {
		content = append(content, textBlock(*item.Output))
	}
	for _, image := range item.Images {
		if imageInput != 0 {
			mime := image.MIME
			if mime == "" {
				mime = "image/png"
			}
			content = append(content, map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "base64", "media_type": mime, "data": image.Data},
			})
		} else {
			content = append(content, textBlock(provider.ImagePlaceholder(image)))
		}
	}
	return map[string]any{"type": "tool_result", "tool_use_id": itemText(item.CallID), "content": content}
}

func textBlock(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

func itemText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func providerIdentity(value string) string {
	if value == "llama.cpp" {
		return "llamacpp"
	}
	return value
}

func BuildBody(ctx provider.Context, providerID, model string, opts BodyOptions) (map[string]any, error) {
	messages := BuildMessages(ctx.Items, providerID, model, opts.AllowEmptySignature, ctx.ImageInput)
	body := map[string]any{
		"model": model, "max_tokens": opts.MaxTokens, "stream": true, "messages": messages,
	}
	if ctx.SystemPrompt != "" {
		block := textBlock(ctx.SystemPrompt)
		if opts.CacheMarkers {
			block["cache_control"] = buildCacheControl(opts.CacheTTL)
		}
		body["system"] = []map[string]any{block}
	}
	if len(ctx.Tools) > 0 {
		tools := make([]map[string]any, 0, len(ctx.Tools))
		for index, tool := range ctx.Tools {
			definition := map[string]any{
				"name": tool.Name, "description": tool.Description,
				"input_schema": provider.BuildToolSchema(tool),
			}
			if opts.CacheMarkers && index == len(ctx.Tools)-1 {
				definition["cache_control"] = buildCacheControl(opts.CacheTTL)
			}
			tools = append(tools, definition)
		}
		body["tools"] = tools
	}
	if opts.CacheMarkers {
		attachCacheToLastMessage(messages, opts.CacheTTL)
	}
	applyThinking(body, ctx.Effort, opts)
	if len(opts.ExtraBody) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(opts.ExtraBody, &extra); err != nil {
			return nil, fmt.Errorf("decode extra_body: %w", err)
		}
		if extra == nil {
			return nil, fmt.Errorf("extra_body must be a JSON object")
		}
		mergeObject(body, extra)
	}
	return body, nil
}

func buildCacheControl(ttl string) map[string]any {
	control := map[string]any{"type": "ephemeral"}
	if strings.EqualFold(ttl, "1h") {
		control["ttl"] = "1h"
	}
	return control
}

func attachCacheToLastMessage(messages []map[string]any, ttl string) {
	if len(messages) == 0 {
		return
	}
	content, ok := messages[len(messages)-1]["content"].([]any)
	if !ok || len(content) == 0 {
		return
	}
	block, ok := content[len(content)-1].(map[string]any)
	if !ok {
		return
	}
	if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
		return
	}
	block["cache_control"] = buildCacheControl(ttl)
}

func applyThinking(body map[string]any, effort *string, opts BodyOptions) {
	switch opts.ThinkingMode {
	case ThinkingOff:
		return
	case ThinkingAdaptive:
		display := "omitted"
		if opts.ShowReasoning {
			display = "summarized"
		}
		body["thinking"] = map[string]any{"type": "adaptive", "display": display}
		if effort != nil && *effort != "" {
			value := *effort
			if !EffortExpressible(value) {
				value = "low"
			}
			body["output_config"] = map[string]any{"effort": value}
		}
	default:
		if opts.MaxTokens < 2 {
			return
		}
		budget := opts.ThinkingBudget
		if budget <= 0 || budget >= opts.MaxTokens {
			budget = opts.MaxTokens - 1
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
}

func mergeObject(target, source map[string]any) {
	for key, value := range source {
		sourceObject, sourceIsObject := value.(map[string]any)
		targetObject, targetIsObject := target[key].(map[string]any)
		if sourceIsObject && targetIsObject {
			mergeObject(targetObject, sourceObject)
			continue
		}
		target[key] = value
	}
}
