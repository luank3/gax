package input

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	terminalocale "github.com/luank3/gax/internal/system/locale"
)

const (
	ansiEraseLine  = "\x1b[K"
	ansiEraseBelow = "\x1b[J"
	ansiDim        = "\x1b[2m"
	ansiBoldOff    = "\x1b[22m"
	ansiSyncBegin  = "\x1b[?2026h"
	ansiSyncEnd    = "\x1b[?2026l"
)

func (editor *Editor) paint() error {
	if editor.Columns <= 0 {
		editor.Columns = 80
	}
	promptWidth := PromptWidth(editor.Prompt)
	layout := editor.Core.Render(promptWidth, promptWidth, editor.Columns, nil)
	clipped := false
	firstRow := 0
	visibleRows := layout.TotalRows
	if editor.Rows >= 3 {
		rowLimit := editAreaRows(editor.Rows)
		clipped = layout.TotalRows > rowLimit
		if clipped {
			visibleRows = rowLimit - 2
			firstRow = WindowTop(editor.windowTop, layout.CursorRow, layout.TotalRows, visibleRows)
		}
	}

	var frame bytes.Buffer
	frame.WriteString(ansiSyncBegin)
	if editor.paintedCursorRow > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", editor.paintedCursorRow)
	}
	frame.WriteByte('\r')
	topIndicatorWidth := 0
	if clipped {
		topIndicatorWidth = appendClipIndicator(&frame, firstRow, editor.Columns)
		frame.WriteString("\r\n")
	}
	if firstRow == 0 {
		frame.WriteString(editor.Prompt)
	}
	paintContext := paintContext{frame: &frame, firstRow: firstRow}
	if clipped {
		editor.Core.RenderWindow(promptWidth, promptWidth, editor.Columns, firstRow,
			firstRow+visibleRows-1, paintContext.emit)
	} else {
		editor.Core.Render(promptWidth, promptWidth, editor.Columns, paintContext.emit)
	}
	editor.ghostPainted = false
	if !clipped && layout.EndColumn < editor.Columns {
		if ghost := editor.ghostText(); ghost != "" {
			fitted := truncateDisplayText(safeDisplayText(ghost), editor.Columns-layout.EndColumn)
			if fitted != "" {
				frame.WriteString(ansiDim)
				frame.WriteString(fitted)
				frame.WriteString(ansiBoldOff)
				editor.ghostPainted = true
			}
		}
	}
	if clipped {
		frame.WriteString(ansiEraseLine + "\r\n")
		appendClipIndicator(&frame, layout.TotalRows-firstRow-visibleRows, editor.Columns)
	}
	frame.WriteString(ansiEraseBelow)
	cursorScreenRow := layout.CursorRow
	endScreenRow := layout.EndRow
	paintedRows := layout.TotalRows
	if clipped {
		cursorScreenRow = layout.CursorRow - firstRow + 1
		endScreenRow = visibleRows + 1
		paintedRows = visibleRows + 2
	}
	if rowsUp := endScreenRow - cursorScreenRow; rowsUp > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", rowsUp)
	}
	frame.WriteByte('\r')
	if layout.CursorColumn > 0 {
		fmt.Fprintf(&frame, "\x1b[%dC", layout.CursorColumn)
	}
	frame.WriteString(ansiSyncEnd)
	if err := writeAll(editor.Writer, frame.Bytes()); err != nil {
		return err
	}
	editor.paintedCursorRow = cursorScreenRow
	editor.paintedRows = paintedRows
	editor.previousPaintClipped = clipped
	editor.windowTop = firstRow
	editor.topIndicatorWidth = topIndicatorWidth
	return nil
}

type paintContext struct {
	frame    *bytes.Buffer
	firstRow int
}

func (context paintContext) emit(event RenderEvent) {
	if event.Kind == RenderGlyph {
		context.frame.WriteString(event.Bytes)
		return
	}
	if event.Row > context.firstRow {
		context.frame.WriteString(ansiEraseLine + "\r\n")
	}
	for column := 0; column < event.Column; column++ {
		context.frame.WriteByte(' ')
	}
}

func editAreaRows(terminalRows int) int {
	cap := terminalRows * 2 / 5
	if cap < 3 {
		cap = 3
	}
	if cap > terminalRows {
		cap = terminalRows
	}
	return cap
}

func appendClipIndicator(frame *bytes.Buffer, hiddenRows, columns int) int {
	return appendClipIndicatorForLocale(frame, hiddenRows, columns, terminalocale.HaveUTF8())
}

func appendClipIndicatorForLocale(frame *bytes.Buffer, hiddenRows, columns int, haveUTF8 bool) int {
	width := 0
	if hiddenRows > 0 {
		marker := "…"
		if !haveUTF8 {
			marker = "..."
		}
		indicator := fmt.Sprintf("%s +%d line", marker, hiddenRows)
		if hiddenRows != 1 {
			indicator += "s"
		}
		width = displayTextWidth(indicator)
		if width < columns {
			frame.WriteString(ansiDim)
			frame.WriteString(indicator)
			frame.WriteString(ansiBoldOff)
		} else {
			width = 0
		}
	}
	frame.WriteString(ansiEraseLine)
	return width
}

func truncateDisplayText(text string, columns int) string {
	if columns <= 0 {
		return ""
	}
	if displayTextWidth(text) <= columns {
		return text
	}
	contentColumns := columns
	suffix := ""
	if columns >= 4 {
		contentColumns -= 3
		suffix = "..."
	}
	var output strings.Builder
	width := 0
	for _, r := range text {
		runeWidth := runeDisplayWidth(r)
		if width+runeWidth > contentColumns {
			break
		}
		output.WriteRune(r)
		width += runeWidth
	}
	output.WriteString(suffix)
	return output.String()
}

func writeAll(writer io.Writer, bytes []byte) error {
	for len(bytes) > 0 {
		written, err := writer.Write(bytes)
		if written > 0 {
			bytes = bytes[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
