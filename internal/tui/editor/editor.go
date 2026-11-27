// Package editor implements a readline-style terminal line editor with
// history recall, word deletion, and bracketed paste support.
package editor

import (
	"strings"
	"unicode"
)

// Buffer holds the editable rune buffer, cursor index, and history recall state.
type Buffer struct {
	runes   []rune
	cursor  int
	history []string
	histIdx int
	draft   string
}

// NewBuffer creates an editor Buffer preloaded with prompt history.
func NewBuffer(history []string) *Buffer {
	return &Buffer{
		history: append([]string(nil), history...),
		histIdx: len(history),
	}
}

// Text returns the current buffer contents.
func (b *Buffer) Text() string { return string(b.runes) }

// Cursor returns the rune cursor position.
func (b *Buffer) Cursor() int { return b.cursor }

// Set replaces the buffer contents and moves cursor to end.
func (b *Buffer) Set(s string) {
	b.runes = []rune(s)
	b.cursor = len(b.runes)
}

// Clear resets the buffer.
func (b *Buffer) Clear() {
	b.runes = nil
	b.cursor = 0
}

// Insert inserts s at the cursor.
func (b *Buffer) Insert(s string) {
	ins := []rune(s)
	next := make([]rune, 0, len(b.runes)+len(ins))
	next = append(next, b.runes[:b.cursor]...)
	next = append(next, ins...)
	next = append(next, b.runes[b.cursor:]...)
	b.runes = next
	b.cursor += len(ins)
}

// Backspace deletes the rune before the cursor.
func (b *Buffer) Backspace() {
	if b.cursor == 0 {
		return
	}
	b.runes = append(b.runes[:b.cursor-1], b.runes[b.cursor:]...)
	b.cursor--
}

// DeleteForward deletes the rune at the cursor.
func (b *Buffer) DeleteForward() {
	if b.cursor >= len(b.runes) {
		return
	}
	b.runes = append(b.runes[:b.cursor], b.runes[b.cursor+1:]...)
}

// DeleteWordBackward implements Ctrl-W / Alt-Backspace.
func (b *Buffer) DeleteWordBackward() {
	if b.cursor == 0 {
		return
	}
	i := b.cursor
	for i > 0 && unicode.IsSpace(b.runes[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(b.runes[i-1]) {
		i--
	}
	b.runes = append(b.runes[:i], b.runes[b.cursor:]...)
	b.cursor = i
}

// DeleteWordForward implements Ctrl-Delete / Alt-D.
func (b *Buffer) DeleteWordForward() {
	if b.cursor >= len(b.runes) {
		return
	}
	i := b.cursor
	for i < len(b.runes) && unicode.IsSpace(b.runes[i]) {
		i++
	}
	for i < len(b.runes) && !unicode.IsSpace(b.runes[i]) {
		i++
	}
	b.runes = append(b.runes[:b.cursor], b.runes[i:]...)
}

// KillToStart implements Ctrl-U.
func (b *Buffer) KillToStart() {
	b.runes = b.runes[b.cursor:]
	b.cursor = 0
}

// KillToEnd implements Ctrl-K.
func (b *Buffer) KillToEnd() {
	b.runes = b.runes[:b.cursor]
}

// MoveLeft moves cursor one rune left.
func (b *Buffer) MoveLeft() {
	if b.cursor > 0 {
		b.cursor--
	}
}

// MoveRight moves cursor one rune right.
func (b *Buffer) MoveRight() {
	if b.cursor < len(b.runes) {
		b.cursor++
	}
}

// MoveHome moves cursor to start.
func (b *Buffer) MoveHome() { b.cursor = 0 }

// MoveEnd moves cursor to end.
func (b *Buffer) MoveEnd() { b.cursor = len(b.runes) }

// HistoryPrev recalls the previous history entry.
func (b *Buffer) HistoryPrev() {
	if len(b.history) == 0 || b.histIdx == 0 {
		return
	}
	if b.histIdx == len(b.history) {
		b.draft = string(b.runes)
	}
	b.histIdx--
	b.Set(b.history[b.histIdx])
}

// HistoryNext advances forward in history.
func (b *Buffer) HistoryNext() {
	if b.histIdx >= len(b.history) {
		return
	}
	b.histIdx++
	if b.histIdx == len(b.history) {
		b.Set(b.draft)
		return
	}
	b.Set(b.history[b.histIdx])
}

// Submit trims surrounding blank lines/whitespace (preserving internal indentation)
// and records non-empty prompts into history.
func (b *Buffer) Submit() string {
	trimmed := strings.TrimSpace(string(b.runes))
	b.Clear()
	if trimmed != "" {
		b.history = append(b.history, trimmed)
		b.histIdx = len(b.history)
	}
	return trimmed
}
