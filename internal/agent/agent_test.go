package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/provider/mock"
	"github.com/luank3/gax/internal/tools"
)

func TestRunLoopToolAndOverflowRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "mock.txt")
	err := os.WriteFile(script, []byte(`
tool read {"path":"hello.txt"}
usage in=10 out=5
end-turn
overflow
end-turn
text compacted summary
end-turn
text final answer
usage in=20 out=4
end-turn
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := mock.New(script, dir)
	if err != nil {
		t.Fatal(err)
	}

	sess := agent.NewSession(core.SessionHeader{Version: 1, ID: "s1", CWD: dir})
	sess.Append(core.Item{Kind: core.KindUser, Text: "read hello.txt"})
	reg := tools.NewRegistry(dir, nil)
	defer reg.Close()

	var events []core.Event
	res := agent.RunLoop(context.Background(), agent.LoopOptions{
		Session:     sess,
		Provider:    prov,
		Tools:       reg,
		Model:       "mock-1",
		CompactAuto: true,
		Sink: core.EventFunc(func(ev core.Event) {
			events = append(events, ev)
		}),
	})

	if res.Outcome != core.OutcomeComplete {
		t.Fatalf("expected OutcomeComplete, got %s (%s)", res.Outcome, res.ErrorMessage)
	}
	if res.FinalText != "final answer" {
		t.Fatalf("unexpected FinalText: %q", res.FinalText)
	}
	if !strings.Contains(sess.ContextWindow()[0].Text, "compacted summary") {
		t.Fatalf("expected compact seed at start of ContextWindow: %+v", sess.ContextWindow())
	}
}

func TestRunLoopAbortDiscardsUnfinishedReasoning(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "abort.txt")
	err := os.WriteFile(script, []byte("reasoning secret thought\ndelay 50\ntext partial\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := mock.New(script, dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := agent.NewSession(core.SessionHeader{Version: 1, ID: "s2", CWD: dir})
	sess.Append(core.Item{Kind: core.KindUser, Text: "go"})

	res := agent.RunLoop(ctx, agent.LoopOptions{
		Session:  sess,
		Provider: prov,
		Model:    "mock-1",
		Sink: core.EventFunc(func(ev core.Event) {
			if _, ok := ev.(core.ReasoningDelta); ok {
				cancel()
			}
		}),
	})
	if res.Outcome != core.OutcomeInterrupted {
		t.Fatalf("expected OutcomeInterrupted, got %s", res.Outcome)
	}
	for _, it := range sess.Items {
		if it.Kind == core.KindReasoning {
			t.Fatalf("unfinished reasoning should be discarded on abort, got %+v", it)
		}
	}
}
