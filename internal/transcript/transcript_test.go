package transcript_test

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/transcript"
)

func TestRenderTranscript(t *testing.T) {
	out := transcript.Render("sys", []core.ToolDef{{Name: "read", Description: "read file"}}, []core.Item{
		{Kind: core.KindUser, Text: "hi"},
		{Kind: core.KindToolCall, CallID: "c1", ToolName: "read", Arguments: `{"path":"a"}`},
		{Kind: core.KindToolResult, CallID: "c1", Output: "ok"},
		{Kind: core.KindAssistant, Text: "done"},
		{Kind: core.KindTurnUsage, Provider: "mock", Model: "m1", Usage: &core.Usage{Input: 10, Output: 5, CostTotal: 0.001}},
	})
	for _, want := range []string{"=== SYSTEM PROMPT ===", "- read: read file", "[user] hi", "[tool_call:read (c1)]", "[assistant] done", "in=10 out=5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
