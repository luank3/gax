package session_test

import (
	"os"
	"testing"
	"time"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/session"
)

func TestSessionRoundTripUndoForkAndCrashTail(t *testing.T) {
	stateDir := t.TempDir()
	cwd := t.TempDir()

	w, err := session.Create(stateDir, cwd, core.SessionHeader{
		Provider: "mock",
		Model:    "m1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = w.AppendItem(core.Item{Kind: core.KindUser, Text: "turn 1"})
	_ = w.AppendItem(core.Item{Kind: core.KindAssistant, Text: "ans 1"})
	_ = w.AppendItem(core.Item{Kind: core.KindTurnBoundary})
	_ = w.AppendItem(core.Item{Kind: core.KindUser, Text: "turn 2"})
	_ = w.AppendItem(core.Item{Kind: core.KindAssistant, Text: "ans 2"})
	_ = w.AppendSelection(session.SelectionSnapshot{Provider: "openai", Model: "gpt-5"})
	_ = w.AppendUndo(1)
	_ = w.Close()

	// Simulate a crash leaving a partial JSON line at EOF.
	f, err := os.OpenFile(w.Path(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":"assistant","text":"trunc`)
	_ = f.Close()

	loaded, err := session.Load(w.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.LiveItems) != 2 || loaded.LiveItems[0].Text != "turn 1" {
		t.Fatalf("expected 2 live items after undo, got %+v", loaded.LiveItems)
	}
	if len(loaded.AllItems) != 5 {
		t.Fatalf("expected 5 total items preserved, got %d", len(loaded.AllItems))
	}
	if loaded.Selection.Provider != "openai" || loaded.Selection.Model != "gpt-5" {
		t.Fatalf("unexpected selection: %+v", loaded.Selection)
	}

	forked, err := session.Fork(w.Path(), stateDir, cwd, "v2", 0)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	_ = forked.Close()
	forkLoaded, err := session.Load(forked.Path())
	if err != nil {
		t.Fatal(err)
	}
	if forkLoaded.Header.ForkedFrom != loaded.Header.ID || !forkLoaded.LiveItems[0].Inherited {
		t.Fatalf("unexpected fork metadata: %+v", forkLoaded.Header)
	}

	// Retention pruning keeps active file even if mtime is old.
	old := time.Now().Add(-60 * 24 * time.Hour)
	_ = os.Chtimes(w.Path(), old, old)
	_ = os.Chtimes(forked.Path(), old, old)
	session.Prune(stateDir, 30, w.Path())
	if _, err := os.Stat(w.Path()); err != nil {
		t.Fatalf("expected keepPath %s to survive prune: %v", w.Path(), err)
	}
	if _, err := os.Stat(forked.Path()); !os.IsNotExist(err) {
		t.Fatalf("expected old unpinned session to be pruned")
	}
}
