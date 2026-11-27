package editor_test

import (
	"testing"

	"github.com/luank3/gax/internal/tui/editor"
)

func TestEditorEditingAndHistory(t *testing.T) {
	b := editor.NewBuffer([]string{"first", "second"})
	b.Insert("hello world")
	b.DeleteWordBackward()
	if b.Text() != "hello " {
		t.Fatalf("after Ctrl-W: %q", b.Text())
	}
	b.Insert("gax")
	b.MoveHome()
	b.DeleteWordForward()
	if b.Text() != " gax" {
		t.Fatalf("after Ctrl-Delete: %q", b.Text())
	}

	b.HistoryPrev()
	if b.Text() != "second" {
		t.Fatalf("expected second, got %q", b.Text())
	}
	b.HistoryPrev()
	if b.Text() != "first" {
		t.Fatalf("expected first, got %q", b.Text())
	}
	b.HistoryNext()
	b.HistoryNext()
	if b.Text() != " gax" {
		t.Fatalf("expected draft restored, got %q", b.Text())
	}

	b.Set("  \n  indented\n  ")
	if got := b.Submit(); got != "indented" {
		t.Fatalf("expected trimmed submit, got %q", got)
	}
}
