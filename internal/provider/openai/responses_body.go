package openai

import (
	"encoding/json"
	"fmt"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
)

// BuildResponsesInputItems translates transcript records into the OpenAI Responses input format.
// Encrypted reasoning is replayed only to the provider/model pair that produced it.
func BuildResponsesInputItems(items []conversation.Item, providerID, model string, imageInput int) []any {
	input := make([]any, 0, len(items))
	for _, item := range items {
		var encoded any
		switch item.Kind {
		case conversation.KindUser:
			encoded = responsesMessage("user", "input_text", stringValue(item.Text))
		case conversation.KindAssistant:
			encoded = responsesMessage("assistant", "output_text", stringValue(item.Text))
		case conversation.KindToolCall:
			encoded = map[string]any{
				"type": "function_call", "call_id": stringValue(item.CallID),
				"name": stringValue(item.ToolName), "arguments": stringValueOr(item.Arguments, "{}"),
			}
		case conversation.KindToolResult:
			output := stringValue(item.Output)
			if len(item.Images) > 0 {
				encodedOutput := make([]map[string]any, 0, len(item.Images)+1)
				if output != "" {
					encodedOutput = append(encodedOutput, map[string]any{"type": "input_text", "text": output})
				}
				for _, image := range item.Images {
					if imageInput != 0 {
						mime := image.MIME
						if mime == "" {
							mime = "image/png"
						}
						encodedOutput = append(encodedOutput, map[string]any{
							"type": "input_image", "image_url": "data:" + mime + ";base64," + image.Data,
						})
					} else {
						encodedOutput = append(encodedOutput, map[string]any{
							"type": "input_text", "text": provider.ImagePlaceholder(image),
						})
					}
				}
				encoded = map[string]any{
					"type": "function_call_output", "call_id": stringValue(item.CallID), "output": encodedOutput,
				}
			} else {
				encoded = map[string]any{
					"type": "function_call_output", "call_id": stringValue(item.CallID), "output": output,
				}
			}
		case conversation.KindReasoning:
			if item.ReasoningJSON == nil || !provenanceMatches(item, providerID, model) || !json.Valid([]byte(*item.ReasoningJSON)) {
				continue
			}
			var reasoning any
			if json.Unmarshal([]byte(*item.ReasoningJSON), &reasoning) != nil {
				continue
			}
			encoded = reasoning
		default:
			// Turn boundaries and usage are bookkeeping records.
			continue
		}
		input = append(input, encoded)
	}
	return input
}

func responsesMessage(role, contentType, text string) map[string]any {
	return map[string]any{
		"type": "message", "role": role,
		"content": []map[string]any{{"type": contentType, "text": text}},
	}
}

// BuildResponsesTools emits flat function declarations, as required by the Responses API.
func BuildResponsesTools(tools []provider.ToolDefinition) []map[string]any {
	definitions := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description,
			"parameters": provider.BuildToolSchema(tool),
		})
	}
	return definitions
}

// BuildResponsesBody assembles a streaming Responses request and applies extra_body last.
func BuildResponsesBody(ctx provider.Context, providerID, model string, opts BodyOptions) (map[string]any, error) {
	body := map[string]any{
		"model":        model,
		"stream":       true,
		"store":        false,
		"instructions": ctx.SystemPrompt,
		"input":        BuildResponsesInputItems(ctx.Items, providerID, model, ctx.ImageInput),
	}
	if len(ctx.Tools) > 0 {
		body["tools"] = BuildResponsesTools(ctx.Tools)
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = true
	}

	effort := ""
	if ctx.Effort != nil {
		effort = *ctx.Effort
	}
	disabled := effort == "none"
	if !disabled {
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if effort != "" {
		reasoning := map[string]any{"effort": effort}
		if !disabled {
			reasoning["summary"] = "auto"
		}
		body["reasoning"] = reasoning
	}
	if opts.SessionCacheKey != nil {
		body["prompt_cache_key"] = *opts.SessionCacheKey
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
