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

func TestEditorUTF8MultilineAndLayout(t *testing.T) {
	b := editor.NewBuffer(nil)
	b.Insert("xin chào\n世界")
	b.MoveLeft()
	b.Insert("！")
	if got := b.Text(); got != "xin chào\n世！界" {
		t.Fatalf("unexpected UTF-8 multiline text: %q", got)
	}
	// Cursor is right before '界' (width of '世' + '！' = 2 + 2 = 4 on row 1)
	cRow, cCol, total := b.Layout(2, 40)
	if cRow != 1 || cCol != 4 || total != 2 {
		t.Fatalf("Layout = (%d,%d,%d), want (1,4,2)", cRow, cCol, total)
	}
	b.MoveWordLeft()
	if b.Cursor() != 9 { // start of line 2 ("世界"...)
		t.Fatalf("MoveWordLeft cursor = %d, want 9", b.Cursor())
	}
	b.MoveWordRight()
	if b.Cursor() != len([]rune(b.Text())) {
		t.Fatalf("MoveWordRight cursor = %d, want end", b.Cursor())
	}
	seq, r, tot := b.RepaintSeq("> ", 40, 1)
	if r != 1 || tot != 2 || seq == "" {
		t.Fatalf("RepaintSeq = (%d,%d), len=%d", r, tot, len(seq))
	}
}
