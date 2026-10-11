package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/provider"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

func TestRenderShowsModelFacingHeaderAndConversation(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindTurnBoundary},
		{Kind: conversation.KindUser, Text: conversation.String("inspect this")},
		{Kind: conversation.KindAssistant, Text: conversation.String("reading")},
		{Kind: conversation.KindToolCall, CallID: conversation.String("c1"), ToolName: conversation.String("read"), Arguments: conversation.String(`{"path":"a.go"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("c1"), Output: conversation.String("contents")},
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("because")},
	}
	ctx := provider.Context{
		SystemPrompt: "system rules",
		Tools:        []provider.ToolDefinition{{Name: "read", Description: "Read a file", Params: []provider.ToolParam{{Name: "path", Type: "string", Required: true}}}},
		Items:        items,
	}
	var output bytes.Buffer
	if err := Render(&output, ctx, ANSI); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"TRANSCRIPT", "system prompt", "system rules", "── tools ──", "[read]", `"path"`, "# turn 1", "inspect this", "reading", "contents", "reasoning", "because"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered transcript missing %q: %s", want, text)
		}
	}
	if strings.Count(text, "contents") != 1 {
		t.Fatalf("paired tool result was rendered more than once: %s", text)
	}
	if !strings.Contains(text, "\x1b[") {
		t.Fatalf("ANSI transcript has no styling: %q", text)
	}
}

func TestTranscriptDecorationsFallBackToASCIIWithoutUTF8(t *testing.T) {
	var output bytes.Buffer
	r := renderer{writer: &output, mode: Plain, palette: terminaltheme.Current(), utf8: false}
	if err := r.renderHeader("", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.section("tools"); err != nil {
		t.Fatal(err)
	}
	if err := r.turnRule(1); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"+", "TRANSCRIPT", "|", "-- tools --", "# turn 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("ASCII transcript omitted %q: %q", want, text)
		}
	}
	for _, forbidden := range []string{"┏", "━", "┃", "──", "─"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("ASCII transcript contains UTF-8 decoration %q: %q", forbidden, text)
		}
	}
}

func TestANSITranscriptUsesConfiguredThemeRoles(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())
	ctx := provider.Context{Items: []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("prompt")},
		{Kind: conversation.KindToolCall, CallID: conversation.String("c1"), ToolName: conversation.String("write")},
		{Kind: conversation.KindToolResult, CallID: conversation.String("c1"), Output: conversation.String("--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new")},
	}}
	var output bytes.Buffer
	if err := Render(&output, ctx, ANSI); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"\x1b[38;5;37m┏", "\x1b[38;5;173mprompt\x1b[39m", "\x1b[38;5;160m-old"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript omitted themed style %q: %q", want, text)
		}
	}
}

func TestRenderPlainModeOmitsControlSequencesAndKeepsRawMessages(t *testing.T) {
	ctx := provider.Context{Items: []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("line one\nline two")},
		{Kind: conversation.KindAssistant, Text: conversation.String("answer")},
	}}
	var output bytes.Buffer
	if err := Render(&output, ctx, Plain); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "\x1b[") || !strings.Contains(text, "line one\nline two") || !strings.Contains(text, "answer") {
		t.Fatalf("plain transcript output = %q", text)
	}
}

func TestRenderToolSchemaPreservesDefinitionOrder(t *testing.T) {
	ctx := provider.Context{Tools: []provider.ToolDefinition{{Name: "ordered", Params: []provider.ToolParam{
		{Name: "zeta", Type: "string", Required: true},
		{Name: "alpha", Type: "boolean"},
	}}}}
	var output bytes.Buffer
	if err := Render(&output, ctx, Plain); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	rootType := strings.Index(text, `"type": "object"`)
	properties := strings.Index(text, `"properties"`)
	zeta := strings.Index(text, `"zeta"`)
	alpha := strings.Index(text, `"alpha"`)
	required := strings.Index(text, `"required"`)
	if rootType < 0 || properties < 0 || zeta < 0 || alpha < 0 || required < 0 ||
		!(rootType < properties && properties < zeta && zeta < alpha && alpha < required) {
		t.Fatalf("schema output did not preserve provider tool order: %s", text)
	}
}

func TestRenderPairsParallelToolResultsInCallOrder(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindToolCall, CallID: conversation.String("a"), ToolName: conversation.String("read"), Arguments: conversation.String(`{"path":"alpha"}`)},
		{Kind: conversation.KindToolCall, CallID: conversation.String("b"), ToolName: conversation.String("read"), Arguments: conversation.String(`{"path":"beta"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("a"), Output: conversation.String("ALPHA")},
		{Kind: conversation.KindToolResult, CallID: conversation.String("b"), Output: conversation.String("BETA")},
	}
	var output bytes.Buffer
	if err := RenderItems(&output, ANSI, items, 0, new(int)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	alpha, alphaResult := strings.Index(text, "alpha"), strings.Index(text, "ALPHA")
	beta, betaResult := strings.Index(text, "beta"), strings.Index(text, "BETA")
	if alpha < 0 || alphaResult < 0 || beta < 0 || betaResult < 0 || !(alpha < alphaResult && alphaResult < beta && beta < betaResult) {
		t.Fatalf("parallel calls and results are not paired in order: %s", text)
	}
	if strings.Count(text, "ALPHA") != 1 || strings.Count(text, "BETA") != 1 {
		t.Fatalf("paired results were duplicated: %s", text)
	}
}

func TestRenderReadResultStylesOnlyLineNumberPrefixes(t *testing.T) {
	items := []conversation.Item{
		{Kind: conversation.KindToolCall, CallID: conversation.String("read"), ToolName: conversation.String("read")},
		{Kind: conversation.KindToolResult, CallID: conversation.String("read"), Output: conversation.String("  12→first line\nnext line")},
	}
	var output bytes.Buffer
	if err := RenderItems(&output, ANSI, items, 0, new(int)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "\x1b[2m  12→\x1b[0mfirst line\nnext line") {
		t.Fatalf("read transcript did not isolate the line-number prefix: %q", text)
	}
}

func TestRenderUsageAndTurnProvenance(t *testing.T) {
	items := []conversation.Item{{
		Kind:     conversation.KindTurnUsage,
		Provider: conversation.String("openai-compatible"), Model: conversation.String("gpt-model"),
		Usage: &conversation.Usage{
			ElapsedMS: conversation.Int64(1550), InputTokens: conversation.Int64(1000),
			UncachedInput: conversation.Int64(800), CachedTokens: conversation.Int64(200),
			OutputTokens: conversation.Int64(24), CostTotal: conversation.Float64(0.003), CostEstimated: true,
			CostInput: conversation.Float64(0.001), CostCacheRead: conversation.Float64(0.0001),
			CostOutput: conversation.Float64(0.002), ProviderLabel: conversation.String("OpenAI"),
			ModelLabel: conversation.String("Readable Model"), Effort: conversation.String("high"),
			ServedModel: conversation.String("wire-model"), Route: conversation.String("proxy"),
		},
	}}
	var output bytes.Buffer
	if err := RenderItems(&output, ANSI, items, 0, new(int)); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"2s · ~$0.0030 · in 800 ~", "cache 200 ~$0.0001", "out 24 ~$0.0020", "OpenAI · Readable Model · high → wire-model via proxy"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage transcript missing %q: %s", want, text)
		}
	}
}

func TestLogSyncAppendsAndResetRewritesContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.md")
	ctx := provider.Context{SystemPrompt: "first prompt"}
	log, err := Open(path, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	items := []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("first turn")}}
	if err := log.Sync(ctx, items); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(ctx, append(items, conversation.Item{Kind: conversation.KindAssistant, Text: conversation.String("answer")})); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "first turn") != 1 || !strings.Contains(string(data), "answer") {
		t.Fatalf("incremental log = %s", data)
	}
	ctx.SystemPrompt = "replacement prompt"
	if err := log.Reset(ctx, []conversation.Item{{Kind: conversation.KindAssistant, Text: conversation.String("new session")}}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "first turn") || !strings.Contains(string(data), "replacement prompt") || !strings.Contains(string(data), "new session") {
		t.Fatalf("reset log retained old transcript or omitted new context: %s", data)
	}
}

func TestInitTruncatesConfiguredTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.md")
	if err := os.WriteFile(path, []byte("old content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("Init() left stale content: %q, %v", data, err)
	}
}
