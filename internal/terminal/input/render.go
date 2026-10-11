package input

import (
	"unicode"
	"unicode/utf8"
)

const pendingGlyphLimit = 256

var (
	unsafeGlyph   = "?"
	inputTabGlyph = "    "
)

type Layout struct {
	CursorRow    int
	CursorColumn int
	EndRow       int
	EndColumn    int
	TotalRows    int
}

type RenderKind uint8

const (
	RenderGlyph RenderKind = iota
	RenderRowBreak
)

type RenderEvent struct {
	Kind   RenderKind
	Bytes  string
	Width  int
	Row    int
	Column int
}

type renderGlyph struct {
	bytes    string
	width    int
	offset   int
	consumed int
	isSpace  bool
}

type renderState struct {
	continuationColumn int
	columns            int
	cursor             int
	emit               func(RenderEvent)
	row                int
	column             int
	cursorRow          int
	cursorColumn       int
	cursorResolved     bool
	pending            []renderGlyph
	pendingWidth       int
	pendingAfterSpace  bool
}

// Render lays out the current buffer and emits its display-safe glyphs and soft/hard row breaks.
// Continuation rows begin at continuationColumn; columns=0 disables soft wrapping.
func (core *Core) Render(promptWidth, continuationColumn, columns int, emit func(RenderEvent)) Layout {
	if core == nil {
		return Layout{TotalRows: 1}
	}
	state := renderState{
		continuationColumn: continuationColumn,
		columns:            columns,
		cursor:             core.cursor,
		emit:               emit,
		column:             promptWidth,
		pending:            make([]renderGlyph, 0, pendingGlyphLimit),
	}
	for offset := 0; offset < len(core.buffer); {
		if core.buffer[offset] == '\n' {
			state.flushPending()
			if !state.cursorResolved && state.cursor == offset {
				state.cursorRow = state.row
				state.cursorColumn = state.column
				state.cursorResolved = true
			}
			state.emitRowBreak()
			offset++
			continue
		}

		glyph := decodeRenderGlyph(core.buffer, offset)
		if glyph.width == 0 {
			if len(state.pending) < pendingGlyphLimit {
				state.pending = append(state.pending, glyph)
			} else {
				state.flushPending()
				state.emitGlyph(glyph)
			}
			offset += glyph.consumed
			continue
		}

		if glyph.isSpace {
			spaceOverflows := state.columns > 0 && state.column+state.pendingWidth+glyph.width >= state.columns
			state.flushPending()
			if spaceOverflows && state.column > state.continuationColumn {
				if !state.cursorResolved && cursorInRenderRange(state.cursor, glyph.offset, glyph.consumed) {
					state.cursorRow = state.row
					state.cursorColumn = state.column
					state.cursorResolved = true
				}
				state.emitRowBreak()
			} else {
				state.emitGlyph(glyph)
				state.pendingAfterSpace = true
			}
			offset += glyph.consumed
			continue
		}

		prospectiveColumn := state.column + state.pendingWidth + glyph.width
		if state.columns > 0 && prospectiveColumn >= state.columns {
			if len(state.pending) > 0 && state.pendingAfterSpace && state.column > state.continuationColumn {
				state.replayPendingOnNewRow()
			} else {
				state.flushPending()
				if state.column > state.continuationColumn {
					state.emitRowBreak()
				}
			}
		}

		if len(state.pending) < pendingGlyphLimit {
			state.pending = append(state.pending, glyph)
			state.pendingWidth += glyph.width
		} else {
			state.flushPending()
			if state.columns > 0 && state.column+glyph.width >= state.columns && state.column > state.continuationColumn {
				state.emitRowBreak()
			}
			state.emitGlyph(glyph)
		}
		offset += glyph.consumed
	}

	if len(state.pending) > 0 && state.columns > 0 && state.column+state.pendingWidth >= state.columns &&
		state.pendingAfterSpace && state.column > state.continuationColumn {
		state.replayPendingOnNewRow()
	} else {
		state.flushPending()
	}
	if !state.cursorResolved {
		state.cursorRow = state.row
		state.cursorColumn = state.column
		state.cursorResolved = true
	}
	return Layout{
		CursorRow: state.cursorRow, CursorColumn: state.cursorColumn,
		EndRow: state.row, EndColumn: state.column, TotalRows: state.row + 1,
	}
}

// RenderWindow emits only rows in the inclusive range [firstRow,lastRow] while returning full layout.
func (core *Core) RenderWindow(promptWidth, continuationColumn, columns, firstRow, lastRow int,
	emit func(RenderEvent)) Layout {
	if emit == nil {
		return core.Render(promptWidth, continuationColumn, columns, nil)
	}
	return core.Render(promptWidth, continuationColumn, columns, func(event RenderEvent) {
		if event.Row >= firstRow && event.Row <= lastRow {
			emit(event)
		}
	})
}

func WindowTop(previous, cursorRow, totalRows, rows int) int {
	if rows <= 0 {
		return 0
	}
	maxTop := totalRows - rows
	if maxTop < 0 {
		maxTop = 0
	}
	top := clamp(previous, 0, maxTop)
	if cursorRow < top {
		top = cursorRow
	} else if cursorRow > top+rows-1 {
		top = cursorRow - (rows - 1)
	}
	return top
}

// PromptWidth counts visible cells up to the first newline, ignoring CSI terminal styling.
func PromptWidth(prompt string) int {
	width := 0
	for index := 0; index < len(prompt); {
		if prompt[index] == '\n' {
			break
		}
		if prompt[index] == 0x1b && index+1 < len(prompt) && prompt[index+1] == '[' {
			index += 2
			for index < len(prompt) && prompt[index] < 0x40 {
				index++
			}
			if index < len(prompt) {
				index++
			}
			continue
		}
		glyph := decodeRenderGlyph([]byte(prompt), index)
		width += glyph.width
		index += glyph.consumed
	}
	return width
}

func decodeRenderGlyph(text []byte, offset int) renderGlyph {
	byteValue := text[offset]
	glyph := renderGlyph{offset: offset, consumed: 1}
	if byteValue == '\t' {
		glyph.bytes = inputTabGlyph
		glyph.width = len(inputTabGlyph)
		return glyph
	}
	if byteValue < 0x20 || byteValue == 0x7f {
		glyph.bytes = unsafeGlyph
		glyph.width = 1
		return glyph
	}
	r, size := utf8.DecodeRune(text[offset:])
	if r == utf8.RuneError && size == 1 && byteValue >= utf8.RuneSelf {
		glyph.bytes = unsafeGlyph
		glyph.width = 1
		return glyph
	}
	glyph.consumed = size
	if unsafeCodepoint(r) {
		glyph.bytes = unsafeGlyph
		glyph.width = 1
		return glyph
	}
	glyph.bytes = string(text[offset : offset+size])
	glyph.width = runeDisplayWidth(r)
	glyph.isSpace = size == 1 && byteValue == ' '
	return glyph
}

func unsafeCodepoint(r rune) bool {
	return unicode.IsControl(r) || r == 0x00ad || r == 0x034f || r == 0x061c || r == 0x115f || r == 0x1160 ||
		r == 0x180e || r >= 0x200b && r <= 0x200f || r >= 0x2028 && r <= 0x2029 ||
		r >= 0x202a && r <= 0x202e || r >= 0x2060 && r <= 0x206f || r == 0x3164 || r == 0xfeff ||
		r == 0xffa0 || r >= 0xfff9 && r <= 0xfffb || r >= 0xe0000 && r <= 0xe007f || unicode.Is(unicode.Cf, r)
}

func cursorInRenderRange(cursor, start, consumed int) bool {
	return cursor >= start && cursor < start+consumed
}

func (state *renderState) emitGlyph(glyph renderGlyph) {
	if !state.cursorResolved && cursorInRenderRange(state.cursor, glyph.offset, glyph.consumed) {
		state.cursorRow = state.row
		state.cursorColumn = state.column
		state.cursorResolved = true
	}
	if state.emit != nil {
		state.emit(RenderEvent{Kind: RenderGlyph, Bytes: glyph.bytes, Width: glyph.width, Row: state.row, Column: state.column})
	}
	state.column += glyph.width
}

func (state *renderState) emitRowBreak() {
	state.row++
	state.column = state.continuationColumn
	if state.emit != nil {
		state.emit(RenderEvent{Kind: RenderRowBreak, Row: state.row, Column: state.column})
	}
}

func (state *renderState) flushPending() {
	for _, glyph := range state.pending {
		state.emitGlyph(glyph)
	}
	state.pending = state.pending[:0]
	state.pendingWidth = 0
	state.pendingAfterSpace = false
}

func (state *renderState) replayPendingOnNewRow() {
	state.emitRowBreak()
	for _, glyph := range state.pending {
		state.emitGlyph(glyph)
	}
	state.pending = state.pending[:0]
	state.pendingWidth = 0
	state.pendingAfterSpace = false
}
