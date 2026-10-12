// Package provider contains the provider-independent request context shared by wire adapters.
package provider

import (
	"fmt"

	"github.com/luank3/gax/internal/conversation"
)

// ToolParam describes one JSON Schema property advertised to a model. A zero Minimum is omitted,
// matching the tool definition format.
type ToolParam struct {
	Name        string
	Type        string
	ItemType    string
	Description string
	Required    bool
	Minimum     int64
}

// ToolDefinition is a model-callable operation and its JSON Schema parameters.
type ToolDefinition struct {
	Name        string
	Description string
	Params      []ToolParam
}

// Context is the provider-facing view of a conversation for one request.
type Context struct {
	SystemPrompt string
	Items        []conversation.Item
	Tools        []ToolDefinition
	Effort       *string
	// ImageInput follows the tri-state convention: 1 supports images, 0 rejects them,
	// and -1 means unknown (treated as supported to avoid silently discarding image content).
	ImageInput int
	SessionID  *string
}

// BuildToolSchema converts a tool's parameter declarations to a JSON Schema object.
func BuildToolSchema(tool ToolDefinition) map[string]any {
	properties := make(map[string]any, len(tool.Params))
	required := make([]string, 0, len(tool.Params))
	for _, param := range tool.Params {
		property := make(map[string]any)
		if param.Type != "" {
			property["type"] = param.Type
		}
		if param.ItemType != "" {
			property["items"] = map[string]any{"type": param.ItemType}
		}
		if param.Description != "" {
			property["description"] = param.Description
		}
		if param.Minimum != 0 {
			property["minimum"] = param.Minimum
		}
		properties[param.Name] = property
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// ImagePlaceholder returns the text fallback used when a wire cannot accept image content.
func ImagePlaceholder(image conversation.Image) string {
	bytes := int64(len(image.Data) / 4 * 3)
	size := fmt.Sprintf("%d bytes", bytes)
	if bytes >= 1024*1024 {
		size = fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
	} else if bytes >= 1024 {
		size = fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	}
	mime := image.MIME
	if mime == "" {
		mime = "image"
	}
	if image.Width > 0 && image.Height > 0 {
		return fmt.Sprintf("[image: %s, %dx%d, %s]", mime, image.Width, image.Height, size)
	}
	return fmt.Sprintf("[image: %s, %s]", mime, size)
}
