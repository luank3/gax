package mock_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider"
	"github.com/luank3/gax/internal/provider/mock"
)

func TestMockScriptDirectives(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script.txt")
	err := os.WriteFile(script, []byte(`
# comment
text hello {{CWD}}\n
space
tool read {"path":"a.go"}
usage in=100 out=20 cached=40 cost=0.01
end-turn
overflow limit=10
text recovered
end-turn
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	c, err := mock.New(script, "/work/proj")
	if err != nil {
		t.Fatal(err)
	}

	var text string
	var tool string
	var usage *core.Usage
	err = c.Stream(context.Background(), provider.StreamRequest{}, func(ev provider.StreamEvent) {
		switch ev.Kind {
		case provider.EventText:
			text += ev.Text
		case provider.EventToolCallDone:
			tool = ev.ToolName
		case provider.EventUsage:
			usage = ev.Usage
		}
	})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if text != "hello /work/proj\n " || tool != "read" || usage == nil || usage.Input != 100 || usage.InTokens != 60 {
		t.Fatalf("unexpected turn 1: text=%q tool=%q usage=%+v", text, tool, usage)
	}

	// Turn 2 overflows when request > 10 tokens, then replays when smaller.
	err = c.Stream(context.Background(), provider.StreamRequest{
		SystemPrompt: "this prompt is definitely longer than forty bytes total",
	}, func(provider.StreamEvent) {})
	var ov *provider.OverflowError
	if !errors.As(err, &ov) {
		t.Fatalf("expected OverflowError, got %v", err)
	}

	var recovered string
	err = c.Stream(context.Background(), provider.StreamRequest{
		SystemPrompt: "short",
	}, func(ev provider.StreamEvent) {
		if ev.Kind == provider.EventText {
			recovered += ev.Text
		}
	})
	if err != nil || recovered != "recovered" {
		t.Fatalf("expected recovered after rewind, err=%v text=%q", err, recovered)
	}
}
