package input

func (editor *Editor) resize(columns, rows int) bool {
	if columns <= 0 {
		columns = editor.Columns
	}
	if columns <= 0 {
		columns = 80
	}
	if rows < 0 {
		rows = 0
	}
	if columns == editor.Columns && rows == editor.Rows {
		return false
	}

	oldColumns := editor.Columns
	editor.Columns = columns
	editor.Rows = rows
	if columns == oldColumns {
		if rows > 0 && editor.paintedCursorRow >= rows {
			editor.paintedCursorRow = rows - 1
		}
		return true
	}

	cursorClimb := 0
	if editor.paintedRows > 0 {
		promptWidth := PromptWidth(editor.Prompt)
		rowWidths, layout := editor.renderedRowWidths(promptWidth, oldColumns)
		firstRow := 0
		if editor.previousPaintClipped {
			firstRow = clamp(editor.windowTop, 0, len(rowWidths))
			if editor.topIndicatorWidth > 0 {
				cursorClimb = (editor.topIndicatorWidth + columns - 1) / columns
			} else {
				cursorClimb = 1
			}
		}
		lastRow := min(layout.CursorRow, len(rowWidths))
		if lastRow > firstRow {
			cursorClimb += reflowPhysicalRows(rowWidths[firstRow:lastRow], columns)
		}
		cursorClimb += layout.CursorColumn / columns
	}
	if rows > 0 && cursorClimb >= rows {
		cursorClimb = rows - 1
	}
	editor.paintedCursorRow = cursorClimb
	editor.paintedRows = 0
	return true
}

func (editor *Editor) renderedRowWidths(promptWidth, columns int) ([]int, Layout) {
	widths := []int{promptWidth}
	layout := editor.Core.Render(promptWidth, promptWidth, columns, func(event RenderEvent) {
		if event.Kind == RenderGlyph {
			endColumn := event.Column + event.Width
			last := len(widths) - 1
			if endColumn > widths[last] {
				widths[last] = endColumn
			}
			return
		}
		widths = append(widths, event.Column)
	})
	return widths, layout
}

func reflowPhysicalRows(rowWidths []int, terminalColumns int) int {
	if terminalColumns <= 0 {
		return 0
	}
	physicalRows := 0
	for _, width := range rowWidths {
		if width > 0 {
			physicalRows += (width + terminalColumns - 1) / terminalColumns
		} else {
			physicalRows++
		}
	}
	return physicalRows
}
