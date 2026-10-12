package mock

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/luank3/gax/internal/conversation"
	coreprovider "github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/transport"
)

func TestScriptedTurnsDecodeDirectivesAndExhaust(t *testing.T) {
	path := writeScript(t, "# comment\n"+
		"text Hello\\nworld\n"+
		"space\n"+
		"text again\n"+
		"reasoning think\\ting\n"+
		"usage future=99 in=10 out=20 cached=3 cache_write=4 cache_write_1h=2 cost=0.5\n"+
		"end-turn\n\n"+
		"tool bash {\"command\":\"ls {{CWD}}\"}\n"+
		"end-turn\n"+
		"text ——————————\n"+
		"end-turn\n")
	provider := New(&path)

	first := collect(t, provider, coreprovider.Context{})
	if got, want := textDeltas(first), "Hello\nworld again"; got != want {
		t.Fatalf("first turn text = %q, want %q", got, want)
	}
	if got, want := reasoningDeltas(first), "think\ting"; got != want {
		t.Fatalf("first turn reasoning = %q, want %q", got, want)
	}
	usage := doneUsage(t, first)
	assertInt64(t, usage.InputTokens, 10)
	assertInt64(t, usage.OutputTokens, 20)
	assertInt64(t, usage.CachedTokens, 3)
	assertInt64(t, usage.CacheWriteTokens, 4)
	assertInt64(t, usage.CacheWrite1HTokens, 2)
	if usage.Cost == nil || *usage.Cost != 0.5 {
		t.Fatalf("cost = %v, want 0.5", usage.Cost)
	}

	second := collect(t, provider, coreprovider.Context{})
	tool := findEvent(second, conversation.EventToolCallStart)
	if tool == nil || tool.Name != "bash" || tool.ID == nil {
		t.Fatalf("tool start = %+v", tool)
	}
	toolDelta := findEvent(second, conversation.EventToolCallDelta)
	if toolDelta == nil {
		t.Fatalf("tool delta = %+v", toolDelta)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolDelta.Delta, `{"command":"ls `+cwd+`"}`; got != want {
		t.Fatalf("tool args = %q, want %q", got, want)
	}
	if end := findEvent(second, conversation.EventToolCallEnd); end == nil || end.ID == nil || *end.ID != *tool.ID {
		t.Fatalf("tool end = %+v, start = %+v", end, tool)
	}

	third := collect(t, provider, coreprovider.Context{})
	if got, want := textDeltas(third), "——————————"; got != want {
		t.Fatalf("third turn text = %q, want %q", got, want)
	}

	exhausted := collect(t, provider, coreprovider.Context{})
	if got, want := textDeltas(exhausted), "Script exhausted — no more turns."; got != want {
		t.Fatalf("exhausted text = %q, want %q", got, want)
	}
}

func TestCancelledScriptTurnDoesNotAdvance(t *testing.T) {
	path := writeScript(t, "text first turn\nend-turn\ntext second turn\nend-turn\n")
	provider := New(&path)
	response, err := provider.Stream(context.Background(), coreprovider.Context{}, func() bool { return true }, nil)
	if !errors.Is(err, transport.ErrCancelled) || !response.Cancelled {
		t.Fatalf("cancelled stream = (%+v, %v), want cancellation", response, err)
	}

	if got := textDeltas(collect(t, provider, coreprovider.Context{})); got != "first turn" {
		t.Fatalf("turn after cancellation = %q, want first turn", got)
	}
}

func TestFinalScriptTurnAtEOFIsFollowedByExhaustion(t *testing.T) {
	path := writeScript(t, "text final turn\n")
	provider := New(&path)
	if got := textDeltas(collect(t, provider, coreprovider.Context{})); got != "final turn" {
		t.Fatalf("final turn text = %q", got)
	}
	if got := textDeltas(collect(t, provider, coreprovider.Context{})); got != "Script exhausted — no more turns." {
		t.Fatalf("turn after EOF = %q", got)
	}
}

func TestMissingScriptEmitsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	provider := New(&path)
	var events []conversation.Event
	_, err := provider.Stream(context.Background(), coreprovider.Context{}, nil, func(event conversation.Event) {
		events = append(events, event)
	})
	if len(events) != 1 || events[0].Kind != conversation.EventError ||
		!strings.Contains(events[0].ErrorMessage, "mock: cannot open '") {
		t.Fatalf("missing-script events = %+v", events)
	}
	if err == nil {
		t.Fatal("missing script should return an error")
	}
}

func TestScriptWarningsMatchDocumentedDiagnostics(t *testing.T) {
	path := writeScript(t, "mystery directive\ntool\nend-turn\n")
	provider := New(&path)
	var diagnostics bytes.Buffer
	provider.SetDiagnosticWriter(&diagnostics)
	collect(t, provider, coreprovider.Context{})
	if got, want := diagnostics.String(), "gax mock: unknown directive: mystery directive\n"+
		"gax mock: 'tool' needs name and JSON args: tool\n"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func TestInteractiveModeEchoesAndSuggestsTools(t *testing.T) {
	provider := New(nil)
	context := coreprovider.Context{
		Items: []conversation.Item{{Kind: conversation.KindUser,
			Text: conversation.String("run `echo \"hi\" C:\\tmp\n\t\x01`")}},
		Tools: []coreprovider.ToolDefinition{{Name: "bash"}},
	}
	events := collect(t, provider, context)
	if got, want := textDeltas(events), "Sure, on it."; got != want {
		t.Fatalf("interactive response = %q, want %q", got, want)
	}
	tool := findEvent(events, conversation.EventToolCallStart)
	args := findEvent(events, conversation.EventToolCallDelta)
	if tool == nil || tool.Name != "bash" || args == nil ||
		args.Delta != `{"command":"echo \"hi\" C:\\tmp\n\t\u0001"}` {
		t.Fatalf("interactive tool events = %+v", events)
	}
	if findEvent(events, conversation.EventDone) == nil {
		t.Fatalf("interactive response did not finish: %+v", events)
	}
}

func TestInteractiveModeSuggestsReadTool(t *testing.T) {
	provider := New(nil)
	context := coreprovider.Context{
		Items: []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("read `docs/notes.md`")}},
		Tools: []coreprovider.ToolDefinition{{Name: "read"}},
	}
	events := collect(t, provider, context)
	tool := findEvent(events, conversation.EventToolCallStart)
	args := findEvent(events, conversation.EventToolCallDelta)
	if tool == nil || tool.Name != "read" || args == nil || args.Delta != `{"path":"docs/notes.md"}` {
		t.Fatalf("interactive read events = %+v", events)
	}
}

func TestInteractiveModeEchoesWhenNoMatchingToolExists(t *testing.T) {
	provider := New(nil)
	context := coreprovider.Context{
		Items: []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("read `docs/notes.md`")}},
		Tools: []coreprovider.ToolDefinition{{Name: "bash"}},
	}
	if got, want := textDeltas(collect(t, provider, context)), "You said: read `docs/notes.md`"; got != want {
		t.Fatalf("interactive echo = %q, want %q", got, want)
	}
}

func TestInteractiveModeAnswersAfterToolResultAndBoundary(t *testing.T) {
	provider := New(nil)
	context := coreprovider.Context{
		Items: []conversation.Item{
			{Kind: conversation.KindUser, Text: conversation.String("run `ls`")},
			{Kind: conversation.KindToolResult, Output: conversation.String("file.go")},
			{Kind: conversation.KindTurnBoundary},
		},
		Tools: []coreprovider.ToolDefinition{{Name: "bash"}},
	}
	if got, want := textDeltas(collect(t, provider, context)), "Tool finished — awaiting next instruction."; got != want {
		t.Fatalf("post-tool response = %q, want %q", got, want)
	}
}

func TestScriptChunksDoNotSplitUTF8(t *testing.T) {
	text := strings.Repeat("界", 12)
	path := writeScript(t, "text "+text+"\nend-turn\n")
	events := collect(t, New(&path), coreprovider.Context{})
	for _, event := range events {
		if event.Kind == conversation.EventTextDelta && !utf8.ValidString(event.Text) {
			t.Fatalf("invalid UTF-8 chunk %q", event.Text)
		}
	}
	if got := textDeltas(events); got != text {
		t.Fatalf("chunked text = %q, want %q", got, text)
	}
}

func writeScript(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func collect(t *testing.T, provider *Provider, request coreprovider.Context) []conversation.Event {
	t.Helper()
	var events []conversation.Event
	response, err := provider.Stream(context.Background(), request, nil, func(event conversation.Event) {
		events = append(events, event)
	})
	if err != nil || response.Cancelled {
		t.Fatalf("mock stream = (%+v, %v)", response, err)
	}
	return events
}

func textDeltas(events []conversation.Event) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Kind == conversation.EventTextDelta {
			builder.WriteString(event.Text)
		}
	}
	return builder.String()
}

func reasoningDeltas(events []conversation.Event) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Kind == conversation.EventReasoningDelta {
			builder.WriteString(event.Text)
		}
	}
	return builder.String()
}

func doneUsage(t *testing.T, events []conversation.Event) *conversation.Usage {
	t.Helper()
	done := findEvent(events, conversation.EventDone)
	if done == nil || done.Usage == nil {
		t.Fatalf("missing done usage: %+v", events)
	}
	return done.Usage
}

func findEvent(events []conversation.Event, kind conversation.EventKind) *conversation.Event {
	for index := range events {
		if events[index].Kind == kind {
			return &events[index]
		}
	}
	return nil
}

func assertInt64(t *testing.T, value *int64, want int64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("value = %v, want %d", value, want)
	}
}
