package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/conversation"
)

func TestShellHeadReadsQuotedExecutableAndArgumentBoundary(t *testing.T) {
	head, end := shellHead("  \"/tmp/path with spaces/less\" -F | cat")
	if head != "/tmp/path with spaces/less" || end != len("  \"/tmp/path with spaces/less\"") {
		t.Fatalf("shellHead() = %q, %d", head, end)
	}
	if got := addLessColorFlag(" less -F", "less", len(" less")); got != " less -R -F" {
		t.Fatalf("addLessColorFlag() = %q", got)
	}
}

func TestResolvePagerRejectsMissingConfiguredCommand(t *testing.T) {
	t.Setenv("PAGER", "gax-pager-that-does-not-exist")
	if _, err := resolvePager(); err == nil || !strings.Contains(err.Error(), "$PAGER") {
		t.Fatalf("resolvePager() error = %v, want a configured pager diagnostic", err)
	}
}

func TestShowTranscriptStreamsModelFacingContextToPager(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.SystemPrompt = "model instructions"
	state.Items = []conversation.Item{{Kind: conversation.KindUser, Text: conversation.String("typed prompt")}}
	var output, diagnostics bytes.Buffer
	showTranscript(state, &output, &diagnostics)
	if !strings.Contains(output.String(), "model instructions") || !strings.Contains(output.String(), "typed prompt") {
		t.Fatalf("pager output omitted transcript context: output=%q diagnostics=%q", output.String(), diagnostics.String())
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("pager reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestHistoryHeadingUsesASCIIWithoutUTF8(t *testing.T) {
	if got, want := historyHeading(2, false), "-- conversation · 2 prompts --"; got != want {
		t.Fatalf("ASCII history heading = %q, want %q", got, want)
	}
}

func TestShowHistoryIncludesCompactedAwayItemsAndHonorsReasoningPreference(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("older prompt")},
		{Kind: conversation.KindAssistant, Text: conversation.String("older answer")},
		{Kind: conversation.KindReasoning, ReasoningText: conversation.String("private reasoning")},
		{Kind: conversation.KindUser, Text: conversation.String("summary seed"), Origin: conversation.OriginCompactSeed},
	}
	var output, diagnostics bytes.Buffer
	showHistory(state, false, &output, &diagnostics)
	plain := stripANSISequences(output.String())
	if !strings.Contains(plain, "conversation · 1 prompt") || !strings.Contains(plain, "▌ older prompt") ||
		!strings.Contains(plain, "older answer") || !strings.Contains(plain, "conversation compacted") ||
		strings.Contains(plain, "summary seed") || strings.Contains(plain, "user:") || strings.Contains(plain, "assistant:") ||
		strings.Contains(output.String(), "private reasoning") {
		t.Fatalf("history view omitted full history or ignored hidden reasoning: %q", output.String())
	}
	output.Reset()
	showHistory(state, true, &output, &diagnostics)
	if !strings.Contains(output.String(), "private reasoning") {
		t.Fatalf("history view omitted enabled reasoning: %q", output.String())
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestShowHistoryRendersToolHeaderAndResultPreview(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{
		{Kind: conversation.KindUser, Text: conversation.String("inspect the source")},
		{Kind: conversation.KindToolCall, CallID: conversation.String("c1"), ToolName: conversation.String("bash"),
			Arguments: conversation.String(`{"command":"printf 'source count'"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("c1"), Output: conversation.String("SOURCE_COUNT_17\n")},
		{Kind: conversation.KindAssistant, Text: conversation.String("There are 17 sources.")},
	}
	var output, diagnostics bytes.Buffer
	showHistory(state, false, &output, &diagnostics)
	plain := stripANSISequences(output.String())
	for _, want := range []string{"▌ inspect the source", "[bash]", "printf 'source count'", "SOURCE_COUNT_17", "There are 17 sources."} {
		if !strings.Contains(plain, want) {
			t.Errorf("history view omitted %q: %q", want, plain)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestShowHistoryKeepsCollapsedToolOutputQuiet(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{
		{Kind: conversation.KindToolCall, CallID: conversation.String("read-1"), ToolName: conversation.String("read"),
			Arguments: conversation.String(`{"path":"/tmp/private.txt"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("read-1"), Output: conversation.String("PRIVATE_FILE_BODY\n")},
	}
	var output, diagnostics bytes.Buffer
	showHistory(state, false, &output, &diagnostics)
	plain := stripANSISequences(output.String())
	if !strings.Contains(plain, "[read]") || !strings.Contains(plain, "private.txt") ||
		strings.Contains(plain, "/tmp/private.txt") || strings.Contains(plain, "PRIVATE_FILE_BODY") {
		t.Fatalf("collapsed read history exposed the wrong details: %q", plain)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestShowHistoryReconstructsSummarizedWritePreview(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{
		{Kind: conversation.KindToolCall, CallID: conversation.String("write-1"), ToolName: conversation.String("write"),
			Arguments: conversation.String(`{"path":"/tmp/new.txt","content":"CREATED_FILE_CONTENT"}`)},
		{Kind: conversation.KindToolResult, CallID: conversation.String("write-1"),
			Output: conversation.String("created /tmp/new.txt (1 line, 20 bytes)"), Origin: conversation.OriginSummarized},
	}
	var output, diagnostics bytes.Buffer
	showHistory(state, false, &output, &diagnostics)
	plain := stripANSISequences(output.String())
	if !strings.Contains(plain, "CREATED_FILE_CONTENT") || strings.Contains(plain, "created /tmp/new.txt") {
		t.Fatalf("summarized write preview was not reconstructed from its call: %q", plain)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestShowHistoryFallsBackToSummaryWhenReconstructedWriteHasNoVisibleRows(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments string
	}{
		{name: "whitespace", arguments: `{"path":"/tmp/empty.txt","content":"   "}`},
		{name: "empty", arguments: `{"path":"/tmp/empty.txt","content":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PAGER", "cat")
			state := agent.NewSession()
			state.Items = []conversation.Item{
				{Kind: conversation.KindToolCall, CallID: conversation.String("write-1"), ToolName: conversation.String("write"),
					Arguments: conversation.String(test.arguments)},
				{Kind: conversation.KindToolResult, CallID: conversation.String("write-1"),
					Output: conversation.String("created /tmp/empty.txt (0 lines, 0 bytes)"), Origin: conversation.OriginSummarized},
			}
			var output, diagnostics bytes.Buffer
			showHistory(state, false, &output, &diagnostics)
			plain := stripANSISequences(output.String())
			if !strings.Contains(plain, "created /tmp/empty.txt") || strings.Contains(plain, "(no changes)") {
				t.Fatalf("empty reconstructed write did not use its result summary: %q", plain)
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
			}
		})
	}
}

func TestShowHistoryRendersInterruptedAssistantMarkerAsSeparateDimBlock(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{{
		Kind: conversation.KindAssistant, Text: conversation.String("partial answer\n[interrupted]"),
		Origin: conversation.OriginInterrupted,
	}}
	var output, diagnostics bytes.Buffer
	showHistory(state, false, &output, &diagnostics)
	plain := stripANSISequences(output.String())
	if !strings.Contains(plain, "partial answer\n\n[interrupted]") {
		t.Fatalf("interruption marker was not separated from the partial answer: %q", plain)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history view reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestShowHistoryResolvesModelCursorControlsBeforePaging(t *testing.T) {
	t.Setenv("PAGER", "cat")
	state := agent.NewSession()
	state.Items = []conversation.Item{{
		Kind: conversation.KindAssistant,
		Text: conversation.String("abcdef\rXY"),
	}}
	var output, diagnostics bytes.Buffer
	showHistory(state, true, &output, &diagnostics)
	if !strings.Contains(output.String(), "XYcdef") || strings.Contains(output.String(), "\r") {
		t.Fatalf("history pager did not resolve cursor output: %q", output.String())
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("history pager reported unexpected diagnostics: %q", diagnostics.String())
	}
}

func TestResolvePagerAddsLessRawColorFlag(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "less")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	t.Setenv("PAGER", "less --quit-if-one-screen")
	got, err := resolvePager()
	if err != nil || got != "less -R --quit-if-one-screen" {
		t.Fatalf("resolvePager() = %q, %v", got, err)
	}
}
