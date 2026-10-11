package picker

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	maxRows        = 12
	titleMaxLines  = 3
	footerMaxLines = 4
	baseRows       = 3
	escapeTimeout  = 50 * time.Millisecond
)

const (
	ansiSyncBegin  = "\x1b[?2026h"
	ansiSyncEnd    = "\x1b[?2026l"
	ansiEraseLine  = "\x1b[K"
	ansiEraseBelow = "\x1b[J"
	ansiCursorHide = "\x1b[?25l"
	ansiCursorShow = "\x1b[?25h"
	ansiBold       = "\x1b[1m"
	ansiDim        = "\x1b[2m"
	ansiBoldOff    = "\x1b[22m"
	ansiFGDefault  = "\x1b[39m"
)

type ByteSource interface {
	ReadByteTimeout(timeout time.Duration) (byte, bool, error)
}

type row struct {
	text  string
	cells int
}

// Run presents an interactive filter using an already-configured raw byte source. It owns picker
// drawing but leaves terminal mode management to the platform-specific TTY adapter.
func Run(options Options, reader ByteSource, writer io.Writer, terminalSize func() (int, int), useUTF8 bool) (int, error) {
	if writer == nil {
		writer = io.Discard
	}
	if len(options.Items) == 0 {
		if options.EmptyMessage != "" {
			return -1, writeAll(writer, []byte(options.EmptyMessage+"\n"))
		}
		return -1, nil
	}
	if reader == nil {
		return -1, errors.New("picker has no byte source")
	}
	core := NewCore(options)
	painted := false
	previousWidths := []int(nil)
	previousPhysicalRows := 0
	if err := writeAll(writer, []byte(ansiCursorHide)); err != nil {
		return -1, err
	}
	selected := -1
	var resultErr error
	paint := func() error {
		columns, rows := pickerTerminalSize(terminalSize)
		width := columns
		if options.DisplayColumns != nil {
			width = options.DisplayColumns(columns)
		}
		if width > columns {
			width = columns
		}
		if width < 1 {
			width = 1
		}
		frameRows, widths, viewport := renderRows(options, core, width, rows, useUTF8)
		core.SetViewportRows(viewport)
		var frame strings.Builder
		frame.WriteString(ansiSyncBegin)
		if painted && previousPhysicalRows > 1 {
			fmt.Fprintf(&frame, "\x1b[%dA", previousPhysicalRows-1)
		}
		if painted {
			frame.WriteByte('\r')
		}
		for index, line := range frameRows {
			if index > 0 {
				frame.WriteString("\r\n")
			}
			frame.WriteString(line)
			frame.WriteString(ansiEraseLine)
		}
		frame.WriteString(ansiEraseBelow)
		frame.WriteString(ansiSyncEnd)
		if err := writeAll(writer, []byte(frame.String())); err != nil {
			return err
		}
		painted = true
		previousWidths = widths
		previousPhysicalRows = physicalRowCount(widths, columns)
		return nil
	}
	cleanup := func() {
		if painted {
			columns, _ := pickerTerminalSize(terminalSize)
			climb := physicalRowCount(previousWidths, columns) - 1
			if climb < 0 {
				climb = 0
			}
			var frame strings.Builder
			frame.WriteString(ansiSyncBegin)
			if climb > 0 {
				fmt.Fprintf(&frame, "\x1b[%dA", climb)
			}
			frame.WriteString("\r")
			frame.WriteString(ansiEraseBelow)
			frame.WriteString(ansiSyncEnd)
			_ = writeAll(writer, []byte(frame.String()))
		}
		_ = writeAll(writer, []byte(ansiCursorShow))
	}
	if err := paint(); err != nil {
		cleanup()
		return -1, err
	}
	for {
		value, available, err := reader.ReadByteTimeout(0)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				resultErr = err
			}
			break
		}
		if !available {
			continue
		}
		accept, cancel := processByte(core, value, reader)
		if cancel {
			break
		}
		if accept {
			if core.selection >= 0 && core.selection < len(core.matches) {
				selected = core.matches[core.selection]
			}
			break
		}
		if err := paint(); err != nil {
			resultErr = err
			break
		}
	}
	cleanup()
	return selected, resultErr
}

func processByte(core *Core, value byte, reader ByteSource) (accept, cancel bool) {
	switch value {
	case 0x03, 0x07:
		return false, true
	case '\r':
		return len(core.matches) > 0, false
	case 0x7f, 0x08:
		length := len(core.query)
		if length > 0 {
			_, size := utf8.DecodeLastRune(core.query)
			core.TruncateQuery(length - size)
		}
		return false, false
	case 0x15:
		core.TruncateQuery(0)
		return false, false
	case 0x17:
		core.TruncateQuery(wordStart(core.query, false))
		return false, false
	case 0x0e, '\n':
		core.Move(Next)
		return false, false
	case 0x10, 0x0b:
		core.Move(Previous)
		return false, false
	case 0x1b:
		readAny := false
		action := terminalinput.DecodeEscape(func() int {
			value, available, err := reader.ReadByteTimeout(escapeTimeout)
			if err != nil || !available {
				return -1
			}
			readAny = true
			return int(value)
		})
		if !readAny {
			return false, true
		}
		applyEscapeAction(core, action)
		return false, false
	default:
		if value >= 0x20 {
			core.AppendQuery(string([]byte{value}))
		}
		return false, false
	}
}

func applyEscapeAction(core *Core, action terminalinput.Action) {
	switch action {
	case terminalinput.ActionHistoryPrev:
		core.Move(Previous)
	case terminalinput.ActionHistoryNext:
		core.Move(Next)
	case terminalinput.ActionLineStart:
		core.SelectFirst()
	case terminalinput.ActionLineEnd:
		core.SelectLast()
	case terminalinput.ActionPageUp:
		core.Page(Previous)
	case terminalinput.ActionPageDown:
		core.Page(Next)
	case terminalinput.ActionKillWordBackAlnum:
		core.TruncateQuery(wordStart(core.query, true))
	}
}

func wordStart(text []byte, alnumOnly bool) int {
	index := len(text)
	if alnumOnly {
		for index > 0 && !wordByte(text[index-1]) {
			index--
		}
		for index > 0 && wordByte(text[index-1]) {
			index--
		}
		return index
	}
	for index > 0 && isSpace(text[index-1]) {
		index--
	}
	for index > 0 && !isSpace(text[index-1]) {
		index--
	}
	return index
}

func wordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\v' || value == '\f'
}

func pickerTerminalSize(query func() (int, int)) (columns, rows int) {
	if query != nil {
		columns, rows = query()
	}
	if columns <= 0 {
		columns = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return columns, rows
}

func renderRows(options Options, core *Core, width, terminalRows int, useUTF8 bool) ([]string, []int, int) {
	titleRows := wrappedLines(options.Title, width, titleMaxLines)
	footerWidth := width - MarkerCells - 1
	if footerWidth < 8 {
		footerWidth = 8
	}
	footerLines := 0
	for _, item := range options.Items {
		lines := len(wrappedLines(item.Description, footerWidth, footerMaxLines))
		if options.RepeatClippedLabel && item.Label != "" && TextCells(item.Label) > LabelCells(item, width) {
			remaining := footerMaxLines - lines
			if remaining > 0 {
				lines += len(wrappedLines(item.Label, footerWidth, remaining))
			}
		}
		if lines > footerLines {
			footerLines = lines
		}
	}
	reserved := baseRows
	if len(titleRows) > 0 {
		reserved += len(titleRows) + 1
	}
	if footerLines > 0 {
		reserved += footerLines + 1
	}
	viewport := terminalRows - reserved
	if viewport < 1 {
		viewport = 1
	}
	if viewport > maxRows {
		viewport = maxRows
	}
	core.SetViewportRows(viewport)

	rows := make([]row, 0, len(titleRows)+2+viewport+footerLines+1)
	for _, line := range titleRows {
		clipped := clipText(line, width, useUTF8)
		rows = append(rows, row{text: ansiBold + clipped.text + ansiBoldOff, cells: clipped.cells})
	}
	if len(titleRows) > 0 {
		rows = append(rows, row{})
	}
	rows = append(rows, row{text: renderSearch(options, core, width, useUTF8), cells: width})
	rows = append(rows, row{})
	if len(core.matches) == 0 {
		rows = append(rows, row{text: ansiDim + "  (no matches)" + ansiBoldOff, cells: min(width, 14)})
	} else {
		end := min(core.firstVisible+viewport, len(core.matches))
		for match := core.firstVisible; match < end; match++ {
			item := options.Items[core.matches[match]]
			line, clipped := renderItem(item, match == core.selection, width, useUTF8)
			rows = append(rows, row{text: line, cells: clipped})
		}
	}
	if footerLines > 0 {
		rows = append(rows, row{})
		item, ok := core.SelectedItem()
		description := ""
		if ok {
			description = item.Description
			if options.RepeatClippedLabel && item.Label != "" && TextCells(item.Label) > LabelCells(item, width) {
				if description != "" {
					description += "\n"
				}
				description += item.Label
			}
		}
		footer := wrappedLines(description, footerWidth, footerLines)
		for index := 0; index < footerLines; index++ {
			if index < len(footer) {
				clipped := clipText(footer[index], footerWidth, useUTF8)
				rows = append(rows, row{text: ansiDim + "  " + clipped.text + ansiBoldOff, cells: clipped.cells + 2})
			} else {
				rows = append(rows, row{})
			}
		}
	}
	frame := make([]string, len(rows))
	widths := make([]int, len(rows))
	for index, line := range rows {
		frame[index] = line.text
		widths[index] = min(line.cells, width)
	}
	return frame, widths, viewport
}

func renderSearch(options Options, core *Core, width int, useUTF8 bool) string {
	icon := "/ "
	if useUTF8 {
		icon = "⌕ "
	}
	textCells := width - MarkerCells
	if textCells < 0 {
		textCells = 0
	}
	if len(core.query) == 0 {
		label := fmt.Sprintf("type to search %d item", len(options.Items))
		if len(options.Items) != 1 {
			label += "s"
		}
		clipped := clipText(label, textCells, useUTF8)
		return ansiDim + icon + clipped.text + ansiBoldOff
	}
	count := fmt.Sprintf("%d/%d", len(core.matches), len(options.Items))
	queryCells := textCells
	showCount := textCells >= terminalwidth.Cells(count)+3
	if showCount {
		queryCells = textCells - terminalwidth.Cells(count) - 2
	}
	clipped := clipText(string(core.query), queryCells, useUTF8)
	var output strings.Builder
	output.WriteString(ansiDim)
	output.WriteString(icon)
	output.WriteString(ansiBoldOff)
	output.WriteString(clipped.text)
	if showCount {
		output.WriteString("  ")
		output.WriteString(ansiDim)
		output.WriteString(count)
		output.WriteString(ansiBoldOff)
	}
	return output.String()
}

func renderItem(item Item, selected bool, width int, useUTF8 bool) (string, int) {
	rowCells := width - MarkerCells
	if rowCells < 1 {
		rowCells = 1
	}
	var output strings.Builder
	if selected {
		output.WriteString(terminaltheme.Open(terminaltheme.Accent))
		if useUTF8 {
			output.WriteString("→ ")
		} else {
			output.WriteString("> ")
		}
		output.WriteString(terminaltheme.Close(terminaltheme.Accent))
	} else {
		output.WriteString("  ")
	}
	currentTag := ""
	if item.Current {
		if useUTF8 {
			currentTag = "✓ current"
		} else {
			currentTag = "* current"
		}
	}
	labelCells := LabelCells(item, width)
	if item.Dim || selected {
		if item.Dim {
			output.WriteString(ansiDim)
		} else {
			output.WriteString(ansiBold)
		}
	}
	labelClip := clipText(item.Label, labelCells, useUTF8)
	if !item.Dim && item.LabelColor != "" {
		output.WriteString(item.LabelColor)
	}
	output.WriteString(labelClip.text)
	if !item.Dim && item.LabelColor != "" {
		output.WriteString(ansiFGDefault)
	}
	if item.Dim || selected {
		output.WriteString(ansiBoldOff)
	}
	cells := MarkerCells + labelClip.cells
	if currentTag != "" {
		output.WriteString("  ")
		output.WriteString(terminaltheme.Open(terminaltheme.OK))
		output.WriteString(currentTag)
		output.WriteString(terminaltheme.Close(terminaltheme.OK))
		cells += CurrentTagCells
	}
	if item.Detail != "" {
		separator := "  "
		separatorCells := DetailSeparatorCells
		if item.Dim {
			separator = " - "
			separatorCells = DimDetailSeparatorCells
			if useUTF8 {
				separator = " – "
			}
		}
		budget := rowCells - labelClip.cells - separatorCells - currentTagCells(item)
		clipped := clipText(item.Detail, budget, useUTF8)
		if clipped.text != "" {
			output.WriteString(ansiDim)
			output.WriteString(separator)
			output.WriteString(clipped.text)
			output.WriteString(ansiBoldOff)
			cells += separatorCells + clipped.cells
		}
	}
	return output.String(), min(cells, width)
}

func currentTagCells(item Item) int {
	if item.Current {
		return CurrentTagCells
	}
	return 0
}

type clippedText struct {
	text  string
	cells int
}

func clipText(text string, budget int, useUTF8 bool) clippedText {
	if budget <= 0 {
		return clippedText{}
	}
	lineEnd := strings.IndexAny(text, "\r\n")
	continued := lineEnd >= 0
	if continued {
		text = text[:lineEnd]
	}
	safe := terminalwidth.FlattenForDisplay(text)
	natural := terminalwidth.Cells(safe)
	clipped := continued || natural > budget
	if !clipped {
		return clippedText{text: safe, cells: natural}
	}
	marker := "."
	if useUTF8 {
		marker = "…"
	}
	contentBudget := budget - 1
	if contentBudget < 0 {
		contentBudget = 0
	}
	var output strings.Builder
	cells := 0
	for _, value := range safe {
		width := terminalwidth.RuneCells(value)
		if cells+width > contentBudget {
			break
		}
		output.WriteRune(value)
		cells += width
	}
	output.WriteString(marker)
	return clippedText{text: output.String(), cells: min(budget, cells+1)}
}

func wrappedLines(text string, width, maxLines int) []string {
	if text == "" || maxLines <= 0 {
		return nil
	}
	lines := terminalwidth.WrapRows(text, 0, width)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
}

func physicalRowCount(widths []int, columns int) int {
	if columns < 1 {
		return len(widths)
	}
	rows := 0
	for _, width := range widths {
		if width > 0 {
			rows += (width + columns - 1) / columns
		} else {
			rows++
		}
	}
	return rows
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
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
