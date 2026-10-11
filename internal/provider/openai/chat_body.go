package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

type ReasoningFormat uint8

const (
	ReasoningFlat ReasoningFormat = iota
	ReasoningNested
)

type BodyOptions struct {
	CacheMarkers    bool
	CacheTTL        string
	SessionCacheKey *string
	ReasoningField  *string
	// ReasoningRequired includes the field on otherwise valid assistant messages even without text to replay.
	ReasoningRequired bool
	ReasoningFormat   ReasoningFormat
	RequestCost       bool
	ExtraBody         json.RawMessage
}

// ParseReasoningFormat accepts the two Chat Completions reasoning shapes. Unknown values preserve
// the supplied fallback.
func ParseReasoningFormat(value string, fallback ReasoningFormat) ReasoningFormat {
	switch {
	case strings.EqualFold(value, "flat"):
		return ReasoningFlat
	case strings.EqualFold(value, "nested"):
		return ReasoningNested
	default:
		return fallback
	}
}

// BuildMessages converts provider-independent transcript items to Chat Completions messages.
// Adjacent assistant, tool-call, and reasoning records are folded into one assistant message.
func BuildMessages(ctx provider.Context, providerID, model string, opts BodyOptions) []map[string]any {
	messages := make([]map[string]any, 0, len(ctx.Items)+1)
	if ctx.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": ctx.SystemPrompt})
	}

	for i := 0; i < len(ctx.Items); {
		item := ctx.Items[i]
		switch item.Kind {
		case conversation.KindUser:
			messages = append(messages, map[string]any{
				"role": "user", "content": stringValue(item.Text),
			})
			i++
		case conversation.KindAssistant, conversation.KindToolCall:
			i = appendAssistantMessage(&messages, ctx.Items, i, opts.ReasoningField,
				opts.ReasoningRequired, providerID, model)
		case conversation.KindToolResult:
			i = appendToolResults(&messages, ctx.Items, i, ctx.ImageInput)
		case conversation.KindReasoning:
			if item.ReasoningText != nil || item.ReasoningJSON != nil {
				i = appendAssistantMessage(&messages, ctx.Items, i, opts.ReasoningField,
					opts.ReasoningRequired, providerID, model)
			} else {
				i++
			}
		default:
			// Turn boundaries and usage records are accounting metadata, not prompt messages.
			i++
		}
	}
	return messages
}

func appendAssistantMessage(messages *[]map[string]any, items []conversation.Item, index int,
	reasoningField *string, reasoningRequired bool, providerID, model string) int {
	var text strings.Builder
	var reasoning strings.Builder
	toolCalls := make([]map[string]any, 0)
	details := make([]json.RawMessage, 0)
	detailsPresent := false

	for index < len(items) {
		item := items[index]
		if item.Kind != conversation.KindAssistant && item.Kind != conversation.KindToolCall && item.Kind != conversation.KindReasoning {
			break
		}
		index++
		switch item.Kind {
		case conversation.KindAssistant:
			if item.Text != nil {
				text.WriteString(*item.Text)
			}
		case conversation.KindToolCall:
			toolCalls = append(toolCalls, buildToolCall(item))
		case conversation.KindReasoning:
			if !provenanceMatches(item, providerID, model) {
				continue
			}
			if item.ReasoningText != nil && *item.ReasoningText != "" {
				if reasoning.Len() > 0 {
					reasoning.WriteByte('\n')
				}
				reasoning.WriteString(*item.ReasoningText)
			}
			if item.ReasoningJSON != nil {
				var blocks []json.RawMessage
				if json.Unmarshal([]byte(*item.ReasoningJSON), &blocks) == nil && blocks != nil {
					detailsPresent = true
					details = append(details, blocks...)
				} else if strings.TrimSpace(*item.ReasoningJSON) == "[]" {
					// An empty typed sequence is still an explicit wire encoding.
					detailsPresent = true
					details = []json.RawMessage{}
				}
			}
		}
	}

	hasReasoningField := reasoningField != nil && *reasoningField != ""
	includeReasoning := hasReasoningField && reasoning.Len() > 0 && !detailsPresent
	if text.Len() == 0 && len(toolCalls) == 0 && !includeReasoning && !detailsPresent {
		return index
	}
	message := map[string]any{"role": "assistant", "content": nil}
	if text.Len() > 0 {
		message["content"] = text.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if detailsPresent {
		message["reasoning_details"] = details
	}
	if includeReasoning || reasoningRequired && hasReasoningField {
		plainReasoning := ""
		if !detailsPresent && reasoning.Len() > 0 {
			plainReasoning = reasoning.String()
		}
		message[*reasoningField] = plainReasoning
	}
	*messages = append(*messages, message)
	return index
}

func buildToolCall(item conversation.Item) map[string]any {
	return map[string]any{
		"id":   stringValue(item.CallID),
		"type": "function",
		"function": map[string]any{
			"name":      stringValue(item.ToolName),
			"arguments": stringValueOr(item.Arguments, "{}"),
		},
	}
}

func appendToolResults(messages *[]map[string]any, items []conversation.Item, index int, imageInput int) int {
	first := index
	for index < len(items) && items[index].Kind == conversation.KindToolResult {
		item := items[index]
		content := stringValue(item.Output)
		if len(item.Images) > 0 && imageInput == 0 {
			var text strings.Builder
			text.WriteString(content)
			for _, image := range item.Images {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(provider.ImagePlaceholder(image))
			}
			content = text.String()
		}
		*messages = append(*messages, map[string]any{
			"role": "tool", "tool_call_id": stringValue(item.CallID), "content": content,
		})
		index++
	}

	if imageInput != 0 {
		parts := make([]map[string]any, 0)
		for i := first; i < index; i++ {
			for _, image := range items[i].Images {
				if len(parts) == 0 {
					parts = append(parts, map[string]any{
						"type": "text", "text": "Image(s) from the preceding tool result(s):",
					})
				}
				mime := image.MIME
				if mime == "" {
					mime = "image/png"
				}
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + mime + ";base64," + image.Data},
				})
			}
		}
		if len(parts) > 0 {
			*messages = append(*messages, map[string]any{"role": "user", "content": parts})
		}
	}
	return index
}

func provenanceMatches(item conversation.Item, providerID, model string) bool {
	if item.Provider == nil || item.Model == nil {
		return false
	}
	return canonicalProviderID(*item.Provider) == canonicalProviderID(providerID) && *item.Model == model
}

func canonicalProviderID(id string) string {
	if id == "llama.cpp" {
		return "llamacpp"
	}
	return id
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringValueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

// BuildBody assembles a Chat Completions request and applies extra_body last. Objects merge
// recursively; arrays and scalar values replace the generated member.
func BuildBody(ctx provider.Context, providerID, model string, opts BodyOptions) (map[string]any, error) {
	messages := BuildMessages(ctx, providerID, model, opts)
	if opts.CacheMarkers {
		ApplyCacheBreakpoints(messages, opts.CacheTTL)
	}
	body := map[string]any{
		"model":          model,
		"stream":         true,
		"messages":       messages,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(ctx.Tools) > 0 {
		tools := make([]map[string]any, 0, len(ctx.Tools))
		for _, tool := range ctx.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": tool.Name, "description": tool.Description, "parameters": provider.BuildToolSchema(tool),
				},
			})
		}
		body["tools"] = tools
	}
	if opts.SessionCacheKey != nil {
		body["prompt_cache_key"] = *opts.SessionCacheKey
	}
	if opts.RequestCost {
		body["usage"] = map[string]any{"include": true}
	}
	if ctx.Effort != nil && *ctx.Effort != "" {
		switch opts.ReasoningFormat {
		case ReasoningNested:
			reasoning := map[string]any{"enabled": *ctx.Effort != "none"}
			if *ctx.Effort != "none" {
				reasoning["effort"] = *ctx.Effort
			}
			body["reasoning"] = reasoning
		default:
			body["reasoning_effort"] = *ctx.Effort
		}
	}
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

// ApplyCacheBreakpoints marks the system prompt and newest cacheable message. A contentless
// assistant message cannot carry a breakpoint, so the search walks backward for content.
func ApplyCacheBreakpoints(messages []map[string]any, ttl string) {
	if len(messages) == 0 {
		return
	}
	tailFloor := 0
	if messages[0]["role"] == "system" && attachCacheControl(messages[0], ttl) {
		tailFloor = 1
	}
	for i := len(messages) - 1; i >= tailFloor; i-- {
		if attachCacheControl(messages[i], ttl) {
			return
		}
	}
}

func attachCacheControl(message map[string]any, ttl string) bool {
	cacheControl := map[string]any{"type": "ephemeral"}
	if strings.EqualFold(ttl, "1h") {
		cacheControl["ttl"] = "1h"
	}
	switch content := message["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		message["content"] = []any{map[string]any{
			"type": "text", "text": content, "cache_control": cacheControl,
		}}
		return true
	case []map[string]any:
		if len(content) == 0 {
			return false
		}
		content[len(content)-1]["cache_control"] = cacheControl
		return true
	case []any:
		if len(content) == 0 {
			return false
		}
		if last, ok := content[len(content)-1].(map[string]any); ok {
			last["cache_control"] = cacheControl
			return true
		}
	}
	return false
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
