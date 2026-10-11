package app

import (
	"context"
	"fmt"
	"io"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
	terminalpaste "github.com/luank3/gax/internal/terminal/paste"
)

func runCopyCommand(ctx context.Context, state *agent.Session, stdout, stderr io.Writer) {
	text := lastAssistantResponse(state)
	if text == "" {
		writeNoteLine(stdout, "no assistant response to copy")
		return
	}
	if err := terminalpaste.CopyClipboard(ctx, text, stdout); err != nil {
		fmt.Fprintf(stderr, "gax: clipboard copy failed: %s\n", err)
		return
	}
	unit := "bytes"
	if len(text) == 1 {
		unit = "byte"
	}
	writeNoteLine(stdout, fmt.Sprintf("copied %d %s to clipboard", len(text), unit))
}

func lastAssistantResponse(state *agent.Session) string {
	if state == nil {
		return ""
	}
	for index := len(state.Items) - 1; index >= 0; index-- {
		item := state.Items[index]
		if item.Kind == conversation.KindAssistant && item.Text != nil && *item.Text != "" {
			return *item.Text
		}
	}
	return ""
}
