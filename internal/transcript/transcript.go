// Package transcript renders the model-facing conversation transcript for Ctrl-T
// and optional GAX_TRANSCRIPT file mirroring.
package transcript

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/luank3/gax/internal/core"
)

// Render formats the model-facing transcript (system prompt, tools, and active items).
func Render(systemPrompt string, tools []core.ToolDef, items []core.Item) string {
	var b strings.Builder
	if systemPrompt != "" {
		b.WriteString("=== SYSTEM PROMPT ===\n")
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
	if len(tools) > 0 {
		b.WriteString("=== TOOLS ===\n")
		for _, t := range tools {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
		}
		b.WriteByte('\n')
	}
	for _, it := range items {
		switch it.Kind {
		case core.KindUser:
			if it.Origin != "" {
				fmt.Fprintf(&b, "[user:%s] %s\n\n", it.Origin, it.Text)
			} else {
				fmt.Fprintf(&b, "[user] %s\n\n", it.Text)
			}
		case core.KindReasoning:
			fmt.Fprintf(&b, "[reasoning] %s\n\n", it.ReasoningText)
		case core.KindAssistant:
			fmt.Fprintf(&b, "[assistant] %s\n\n", it.Text)
		case core.KindToolCall:
			fmt.Fprintf(&b, "[tool_call:%s (%s)] %s\n", it.ToolName, it.CallID, it.Arguments)
		case core.KindToolResult:
			fmt.Fprintf(&b, "[tool_result:%s] %s\n\n", it.CallID, it.Output)
		case core.KindTurnUsage:
			if it.Usage != nil {
				fmt.Fprintf(&b, "--- usage: in=%d out=%d cached=%d cost=$%.4f (%s/%s) ---\n\n",
					it.Usage.Input, it.Usage.Output, it.Usage.Cached, it.Usage.CostTotal, it.Provider, it.Model)
			}
		}
	}
	return b.String()
}

// Writer mirrors transcript snapshots to a file.
type Writer struct {
	mu   sync.Mutex
	path string
}

// NewWriter creates a transcript writer and truncates path.
func NewWriter(path string) (*Writer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return nil, err
	}
	return &Writer{path: path}, nil
}

// Write replaces the transcript file with the latest rendered transcript.
func (w *Writer) Write(systemPrompt string, tools []core.ToolDef, items []core.Item) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return os.WriteFile(w.path, []byte(Render(systemPrompt, tools, items)), 0o600)
}
