package input_test

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/luank3/gax/internal/terminal/input"
)

func TestCoreEditsAndMovesAcrossUTF8(t *testing.T) {
	core := input.NewCore()
	core.SetBuffer("héllo")
	if core.Text() != "héllo" || core.Cursor() != len("héllo") {
		t.Fatalf("initial buffer/cursor = %q/%d", core.Text(), core.Cursor())
	}
	core.MoveLeft()
	core.MoveLeft()
	if core.Cursor() != len("hél") {
		t.Fatalf("cursor after UTF-8 motion = %d, want %d", core.Cursor(), len("hél"))
	}
	core.Insert("XX")
	if core.Text() != "hélXXlo" || core.Cursor() != len("hélXX") {
		t.Fatalf("insert result = %q at %d", core.Text(), core.Cursor())
	}
	replacement := "!"
	core.ReplaceSpan(len("hél"), len("hélXX"), &replacement)
	if core.Text() != "hél!lo" || core.Cursor() != len("hél!") {
		t.Fatalf("replace result = %q at %d", core.Text(), core.Cursor())
	}
	core.SetCursor(0)
	core.DeleteBack()
	core.DeleteForward()
	if core.Text() != "él!lo" || core.Cursor() != 0 {
		t.Fatalf("delete result = %q at %d", core.Text(), core.Cursor())
	}
}

func TestCoreMovesBytewiseAcrossMalformedUTF8(t *testing.T) {
	cases := []struct {
		text       string
		cursor     int
		direction  string
		wantCursor int
	}{
		{"\xc3(", 0, "right", 1},
		{"\xc0\x80", 0, "right", 1},
		{"A\x80", 2, "left", 1},
		{"\xed\xa0\x80", 3, "left", 2},
		{"\xf5\x80\x80\x80", 4, "left", 3},
	}
	for _, test := range cases {
		core := input.NewCore()
		core.SetBuffer(test.text)
		core.SetCursor(test.cursor)
		if test.direction == "left" {
			core.MoveLeft()
		} else {
			core.MoveRight()
		}
		if core.Cursor() != test.wantCursor {
			t.Errorf("motion in %q from %d %s = %d, want %d", test.text, test.cursor, test.direction, core.Cursor(), test.wantCursor)
		}
	}
}

func TestCoreWordEditingUsesWhitespaceAndAlphanumericBoundaries(t *testing.T) {
	core := input.NewCore()
	core.SetBuffer("foo/bar  héllo")
	core.MoveWordLeft()
	if core.Cursor() != len("foo/bar  ") {
		t.Fatalf("word-left cursor = %d, want %d", core.Cursor(), len("foo/bar  "))
	}
	core.KillWordBackAlnum()
	if core.Text() != "foo/héllo" {
		t.Fatalf("alphanumeric word kill = %q", core.Text())
	}
	core.SetBuffer("hello world")
	core.KillWordBack()
	if core.Text() != "hello " {
		t.Fatalf("whitespace word kill = %q", core.Text())
	}
	core.SetBuffer("foo/bar.baz")
	core.SetCursor(0)
	core.MoveWordRight()
	if core.Cursor() != 3 {
		t.Fatalf("word-right cursor = %d, want 3", core.Cursor())
	}
	core.KillWordForward()
	if core.Text() != "foo.baz" {
		t.Fatalf("forward word kill = %q", core.Text())
	}
}

func TestCoreHistoryDeduplicatesSearchesAndRestoresDraft(t *testing.T) {
	core := input.NewCore()
	if !core.AddHistory("git status") || core.AddHistory("git status") || !core.AddHistory("make build") ||
		!core.AddHistory("git commit -m wip") || !core.AddHistory("make test") {
		t.Fatal("history additions did not apply erasedups semantics")
	}
	if got := core.HistorySearch("git", core.HistoryLen()-1, -1); got != 2 {
		t.Fatalf("reverse history search = %d, want 2", got)
	}
	if got := core.HistorySearch("git", 1, -1); got != 0 {
		t.Fatalf("bounded reverse history search = %d, want 0", got)
	}
	core.SetBuffer("my draft")
	core.HistoryPrev()
	if core.Text() != "make test" {
		t.Fatalf("previous history entry = %q", core.Text())
	}
	core.Insert("!")
	core.HistoryPrev()
	if core.Text() != "git commit -m wip" {
		t.Fatalf("second previous history entry = %q", core.Text())
	}
	core.HistoryNext()
	if core.Text() != "make test" {
		t.Fatalf("next history entry = %q", core.Text())
	}
	core.HistoryNext()
	if core.Text() != "my draft" {
		t.Fatalf("history did not restore the draft: %q", core.Text())
	}
	if got := core.HistoryEntries(); !reflect.DeepEqual(got, []string{"git status", "make build", "git commit -m wip", "make test"}) {
		t.Fatalf("editing recalled history changed stored entries: %v", got)
	}
}

func TestCoreHistoryEvictsOldestAtCapacity(t *testing.T) {
	core := input.NewCore()
	for index := 0; index <= input.HistoryLimit; index++ {
		core.AddHistory("e" + strconv.Itoa(index))
	}
	entries := core.HistoryEntries()
	if len(entries) != input.HistoryLimit || entries[0] != "e1" || entries[len(entries)-1] != "e1000" {
		t.Fatalf("history bounds differ: count=%d first=%q last=%q", len(entries), entries[0], entries[len(entries)-1])
	}
}

func TestCoreAcceptHistoryPreservesDraftForDownNavigation(t *testing.T) {
	core := input.NewCore()
	core.AddHistory("first")
	core.AddHistory("second")
	core.SetBuffer("draft")
	if !core.AcceptHistory(0, "draft", true) {
		t.Fatal("failed to accept history result")
	}
	core.HistoryNext()
	if core.Text() != "second" {
		t.Fatalf("next history entry = %q, want second", core.Text())
	}
	core.HistoryNext()
	if core.Text() != "draft" {
		t.Fatalf("live draft = %q, want draft", core.Text())
	}
}

func TestHistoryEncodingEscapesOnlyBackslashAndNewline(t *testing.T) {
	raw := "line1\nline2\\tab\n"
	encoded := input.EncodeHistory(raw)
	if encoded != "line1\\nline2\\\\tab\\n" {
		t.Fatalf("encoded history = %q", encoded)
	}
	if got := input.DecodeHistory(encoded); got != raw {
		t.Fatalf("decoded history = %q, want %q", got, raw)
	}
	if got := input.DecodeHistory(`a\xb`); got != `a\xb` {
		t.Fatalf("unknown escape was changed: %q", got)
	}
}

func TestDecodeEscapeMapsCursorEditingAndPasteSequences(t *testing.T) {
	cases := []struct {
		sequence string
		want     input.Action
	}{
		{"\x1b[D", input.ActionMoveLeft},
		{"\x1b[C", input.ActionMoveRight},
		{"\x1b[1;5D", input.ActionMoveWordLeft},
		{"\x1b[1;3C", input.ActionMoveWordRight},
		{"\x1b[1;1D", input.ActionMoveLeft},
		{"\x1b[1;2D", input.ActionMoveLeft},
		{"\x1b[1;5;2D", input.ActionMoveWordLeft},
		{"\x1bO1;5D", input.ActionMoveWordLeft},
		{"\x1b[H", input.ActionLineStart},
		{"\x1b[F", input.ActionLineEnd},
		{"\x1b[3~", input.ActionDeleteForward},
		{"\x1b[3;5~", input.ActionDeleteForward},
		{"\x1b[7^", input.ActionLineStart},
		{"\x1b[8$", input.ActionLineEnd},
		{"\x1b[3@", input.ActionDeleteForward},
		{"\x1b[200~", input.ActionPasteBegin},
		{"\x1b[5~", input.ActionPageUp},
		{"\x1b[6~", input.ActionPageDown},
		{"\x1b[a", input.ActionHistoryPrev},
		{"\x1b[c", input.ActionMoveRight},
		{"\x1bOc", input.ActionMoveWordRight},
		{"\x1bOd", input.ActionMoveWordLeft},
		{"\x1b[11~", input.ActionNone},
		{"\x1bOb", input.ActionHistoryNext},
		{"\x1bOf", input.ActionNone},
		{"\x1bOa", input.ActionHistoryPrev},
		{"\x1bb", input.ActionMoveWordLeft},
		{"\x1bf", input.ActionMoveWordRight},
		{"\x1bd", input.ActionKillWordForward},
		{"\x1b\x7f", input.ActionKillWordBackAlnum},
		{"\x1b\r", input.ActionInsertNewline},
		{"\x1b\x1b[D", input.ActionMoveWordLeft},
		{"\x1b\x1b\x1b[C", input.ActionMoveWordRight},
		{"\x1b[", input.ActionNone},
	}
	for _, test := range cases {
		t.Run(test.sequence, func(t *testing.T) {
			if got := decodeEscape(test.sequence); got != test.want {
				t.Fatalf("DecodeEscape(%q) = %v, want %v", test.sequence, got, test.want)
			}
		})
	}
}

func decodeEscape(sequence string) input.Action {
	position := 1 // DecodeEscape is called after the leading ESC was consumed.
	return input.DecodeEscape(func() int {
		if position >= len(sequence) {
			return -1
		}
		value := sequence[position]
		position++
		return int(value)
	})
}
