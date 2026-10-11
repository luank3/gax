// Package vtresolve settles cursor-addressed terminal output for safe pager display.
package vtresolve

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	maxCursorColumn = 4096
	maxCSIParameter = 1_000_000
)

type segmentKind uint8

const (
	glyphSegment segmentKind = iota
	controlSegment
)

type rowSegment struct {
	kind  segmentKind
	bytes []byte
	width int
}

type terminalRow struct {
	segments []rowSegment
	cursor   int
}

type escapeKind uint8

const (
	otherEscape escapeKind = iota
	csiEscape
)

type escapeSequence struct {
	kind     escapeKind
	byteLen  int
	final    byte
	first    int
	hasFirst bool
}

type sgrState struct {
	bold, dim, italic, underline   bool
	blink, inverse, hidden, strike bool
	foreground, background         string
}

// Resolve replays supported cursor controls against one terminal row at a time. LF, CR, CSI
// cursor-left/right, CSI erase-line, and SGR are interpreted; other complete escape sequences
// remain zero-width controls. A nonempty final row receives a trailing LF.
func Resolve(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}
	var output bytes.Buffer
	row := terminalRow{}
	style := sgrState{}
	carried := sgrState{}
	for offset := 0; offset < len(input); {
		value := input[offset]
		switch value {
		case '\n':
			commitRow(&output, &row, &carried, style)
			offset++
			continue
		case '\r':
			row.cursor = 0
			offset++
			continue
		case 0x1b:
			escape := parseEscape(input[offset:])
			if escape.kind == csiEscape {
				switch escape.final {
				case 'D':
					distance := 1
					if escape.hasFirst && escape.first > 0 {
						distance = escape.first
					}
					row.cursor -= distance
					if row.cursor < 0 {
						row.cursor = 0
					}
				case 'C':
					distance := 1
					if escape.hasFirst && escape.first > 0 {
						distance = escape.first
					}
					row.cursor += distance
					if row.cursor > maxCursorColumn {
						row.cursor = maxCursorColumn
					}
				case 'K':
					mode := 0
					if escape.hasFirst {
						mode = escape.first
					}
					eraseLine(&row, mode)
				case 'm':
					applySGR(&style, input[offset:offset+escape.byteLen])
					row.writeControl(input[offset : offset+escape.byteLen])
				default:
					row.writeControl(input[offset : offset+escape.byteLen])
				}
			} else {
				row.writeControl(input[offset : offset+escape.byteLen])
			}
			offset += escape.byteLen
			continue
		}

		runeValue, size := utf8.DecodeRune(input[offset:])
		if size == 0 {
			size = 1
		}
		width := terminalwidth.RuneCells(runeValue)
		if width == 0 {
			row.writeCombining(input[offset : offset+size])
		} else {
			if width < 0 {
				width = 1
			}
			row.writeGlyph(input[offset:offset+size], width)
		}
		offset += size
	}
	if len(row.segments) > 0 {
		commitRow(&output, &row, &carried, style)
	}
	row.free()
	return output.Bytes()
}

func (row *terminalRow) reset() {
	clear(row.segments)
	row.segments = row.segments[:0]
	row.cursor = 0
}

func (row *terminalRow) free() {
	row.reset()
	row.segments = nil
}

func (row *terminalRow) insert(index int, kind segmentKind, text []byte, width int) {
	if index < 0 {
		index = 0
	}
	if index > len(row.segments) {
		index = len(row.segments)
	}
	row.segments = append(row.segments, rowSegment{})
	copy(row.segments[index+1:], row.segments[index:len(row.segments)-1])
	row.segments[index] = rowSegment{kind: kind, bytes: append([]byte(nil), text...), width: width}
}

func (row *terminalRow) width() int {
	width := 0
	for _, segment := range row.segments {
		width += segment.width
	}
	return width
}

func (row *terminalRow) splitAtColumn(column int) int {
	currentColumn := 0
	for index := 0; index < len(row.segments); index++ {
		segment := &row.segments[index]
		if segment.width == 0 {
			if segment.kind == glyphSegment && currentColumn == column {
				return index
			}
			continue
		}
		if currentColumn == column {
			return index
		}
		if currentColumn+segment.width > column {
			headWidth := 0
			originalWidth := segment.width
			offset := segmentOffsetAtColumn(segment.bytes, column-currentColumn, &headWidth)
			if offset == 0 {
				return index
			}
			tail := append([]byte(nil), segment.bytes[offset:]...)
			segment.bytes = segment.bytes[:offset]
			segment.width = headWidth
			row.insert(index+1, glyphSegment, tail, originalWidth-headWidth)
			return index + 1
		}
		currentColumn += segment.width
	}
	return len(row.segments)
}

func segmentOffsetAtColumn(text []byte, column int, cellsBefore *int) int {
	offset := 0
	currentColumn := 0
	for offset < len(text) {
		runeValue, size := utf8.DecodeRune(text[offset:])
		if size == 0 {
			size = 1
		}
		width := terminalwidth.RuneCells(runeValue)
		if width < 0 {
			width = 1
		}
		if width > 0 && (currentColumn >= column || currentColumn+width > column) {
			break
		}
		currentColumn += width
		offset += size
	}
	*cellsBefore = currentColumn
	return offset
}

func (row *terminalRow) zeroWidthInsertIndex() int {
	if row.cursor >= row.width() {
		return len(row.segments)
	}
	index := row.splitAtColumn(row.cursor)
	for index < len(row.segments) && row.segments[index].width == 0 {
		index++
	}
	return index
}

func (row *terminalRow) removeGlyphs(start, end int) int {
	if start < 0 {
		start = 0
	}
	if end > len(row.segments) {
		end = len(row.segments)
	}
	if start >= end {
		return start
	}
	keptEnd := start
	for index := start; index < end; index++ {
		if row.segments[index].kind == controlSegment {
			row.segments[keptEnd] = row.segments[index]
			keptEnd++
		}
	}
	copy(row.segments[keptEnd:], row.segments[end:])
	newLength := len(row.segments) - (end - keptEnd)
	clear(row.segments[newLength:])
	row.segments = row.segments[:newLength]
	return keptEnd
}

func (row *terminalRow) padToCursor(width int) {
	if row.cursor <= width {
		return
	}
	paddingLength := row.cursor - width
	row.insert(len(row.segments), glyphSegment, []byte(strings.Repeat(" ", paddingLength)), paddingLength)
}

func (row *terminalRow) writeGlyph(text []byte, width int) {
	rowWidth := row.width()
	if row.cursor >= rowWidth {
		row.padToCursor(rowWidth)
		if count := len(row.segments); count > 0 && row.segments[count-1].width > 0 {
			last := &row.segments[count-1]
			last.bytes = append(last.bytes, text...)
			last.width += width
		} else {
			row.insert(len(row.segments), glyphSegment, text, width)
		}
		row.cursor += width
		return
	}
	start := row.splitAtColumn(row.cursor)
	end := row.splitAtColumn(row.cursor + width)
	row.removeGlyphs(start, end)
	row.insert(start, glyphSegment, text, width)
	row.cursor += width
}

func (row *terminalRow) writeControl(text []byte) {
	row.insert(row.zeroWidthInsertIndex(), controlSegment, text, 0)
}

func (row *terminalRow) writeCombining(text []byte) {
	width := row.width()
	count := len(row.segments)
	if row.cursor == width && count > 0 && row.segments[count-1].kind == glyphSegment && row.segments[count-1].width > 0 {
		row.segments[count-1].bytes = append(row.segments[count-1].bytes, text...)
		return
	}
	row.insert(row.zeroWidthInsertIndex(), glyphSegment, text, 0)
}

func eraseLine(row *terminalRow, mode int) {
	width := row.width()
	fromColumn := row.cursor
	if mode == 1 || mode == 2 {
		fromColumn = 0
	}
	toColumn := width
	if mode == 1 {
		toColumn = row.cursor + 1
	}
	if toColumn > width {
		toColumn = width
	}
	if fromColumn >= toColumn {
		return
	}
	start := row.splitAtColumn(fromColumn)
	end := row.splitAtColumn(toColumn)
	keptEnd := row.removeGlyphs(start, end)
	if mode != 1 {
		return
	}
	for index := keptEnd; index < len(row.segments); index++ {
		if row.segments[index].width > 0 {
			paddingLength := toColumn - fromColumn
			row.insert(keptEnd, glyphSegment, []byte(strings.Repeat(" ", paddingLength)), paddingLength)
			return
		}
	}
}

func rowHasVisibleGlyphs(row *terminalRow) bool {
	for _, segment := range row.segments {
		if segment.width > 0 {
			return true
		}
	}
	return false
}

func commitRow(output *bytes.Buffer, row *terminalRow, carried *sgrState, current sgrState) {
	restyle := rowHasVisibleGlyphs(row)
	if restyle && !carried.isDefault() {
		output.WriteString(carried.sequence())
	}
	for _, segment := range row.segments {
		output.Write(segment.bytes)
	}
	if restyle && !current.isDefault() {
		output.WriteString("\x1b[0m")
	}
	output.WriteByte('\n')
	row.reset()
	*carried = current
}

func parseCSI(input []byte, escape *escapeSequence) {
	readingFirst := true
	for index := 2; index < len(input); index++ {
		value := input[index]
		switch {
		case value >= '0' && value <= '9':
			if readingFirst {
				digit := int(value - '0')
				if escape.first > (maxCSIParameter-digit)/10 {
					escape.first = maxCSIParameter
				} else {
					escape.first = escape.first*10 + digit
				}
				escape.hasFirst = true
			}
		case value == ';' || value == ':':
			readingFirst = false
		case value == '?':
			// Private CSI introducer does not end the first-parameter scan.
		case value >= 0x40 && value <= 0x7e:
			escape.kind = csiEscape
			escape.final = value
			escape.byteLen = index + 1
			return
		}
	}
}

func parseEscape(input []byte) escapeSequence {
	escape := escapeSequence{byteLen: len(input)}
	if len(input) < 2 {
		return escape
	}
	switch input[1] {
	case '[':
		parseCSI(input, &escape)
	case ']':
		for index := 2; index < len(input); index++ {
			if input[index] == '\a' {
				escape.byteLen = index + 1
				return escape
			}
			if input[index] == 0x1b && index+1 < len(input) && input[index+1] == '\\' {
				escape.byteLen = index + 2
				return escape
			}
		}
	default:
		escape.byteLen = 2
	}
	return escape
}

func (state sgrState) isDefault() bool {
	return !state.bold && !state.dim && !state.italic && !state.underline && !state.blink &&
		!state.inverse && !state.hidden && !state.strike && state.foreground == "" && state.background == ""
}

func (state sgrState) sequence() string {
	codes := make([]string, 0, 10)
	if state.bold {
		codes = append(codes, "1")
	}
	if state.dim {
		codes = append(codes, "2")
	}
	if state.italic {
		codes = append(codes, "3")
	}
	if state.underline {
		codes = append(codes, "4")
	}
	if state.blink {
		codes = append(codes, "5")
	}
	if state.inverse {
		codes = append(codes, "7")
	}
	if state.hidden {
		codes = append(codes, "8")
	}
	if state.strike {
		codes = append(codes, "9")
	}
	if state.foreground != "" {
		codes = append(codes, state.foreground)
	}
	if state.background != "" {
		codes = append(codes, state.background)
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func applySGR(state *sgrState, sequence []byte) {
	if len(sequence) < 3 {
		return
	}
	params := sequence[2 : len(sequence)-1]
	if len(params) > 0 && (params[0] == '<' || params[0] == '=' || params[0] == '>' || params[0] == '?') {
		return
	}
	for start := 0; start <= len(params); {
		end := sgrTokenEnd(params, start)
		code := sgrTokenValue(params[start:end])
		colon := bytes.IndexByte(params[start:end], ':')
		if code == 38 || code == 48 {
			spanEnd := end
			if colon < 0 {
				kindStart := end + 1
				kindEnd := sgrTokenEnd(params, kindStart)
				kind := 0
				if kindStart <= len(params) {
					kind = sgrTokenValue(params[kindStart:kindEnd])
				}
				payloadTokens := 0
				if kind == 5 {
					payloadTokens = 2
				} else if kind == 2 {
					payloadTokens = 4
				}
				for count := 0; count < payloadTokens && spanEnd < len(params); count++ {
					spanEnd = sgrTokenEnd(params, spanEnd+1)
				}
			}
			color := string(params[start:spanEnd])
			if len(color) >= 20 {
				color = ""
			}
			if code == 38 {
				state.foreground = color
			} else {
				state.background = color
			}
			start = spanEnd + 1
			continue
		}
		if code == 4 && colon >= 0 {
			colonIndex := start + colon
			state.underline = colonIndex+1 >= len(params) || params[colonIndex+1] != '0'
		} else if code >= 0 {
			applySGRCode(state, code)
		}
		start = end + 1
	}
}

func sgrTokenEnd(params []byte, start int) int {
	for start < len(params) && params[start] != ';' {
		start++
	}
	return start
}

func sgrTokenValue(token []byte) int {
	if len(token) > 0 && (token[0] < '0' || token[0] > '9') {
		return -1
	}
	value := 0
	for _, digit := range token {
		if digit < '0' || digit > '9' {
			break
		}
		value = value*10 + int(digit-'0')
		if value > 9999 {
			return 9999
		}
	}
	return value
}

func applySGRCode(state *sgrState, code int) {
	switch code {
	case 0:
		*state = sgrState{}
	case 1:
		state.bold = true
	case 2:
		state.dim = true
	case 3:
		state.italic = true
	case 4:
		state.underline = true
	case 5, 6:
		state.blink = true
	case 7:
		state.inverse = true
	case 8:
		state.hidden = true
	case 9:
		state.strike = true
	case 22:
		state.bold, state.dim = false, false
	case 23:
		state.italic = false
	case 24:
		state.underline = false
	case 25:
		state.blink = false
	case 27:
		state.inverse = false
	case 28:
		state.hidden = false
	case 29:
		state.strike = false
	case 39:
		state.foreground = ""
	case 49:
		state.background = ""
	default:
		if code >= 30 && code <= 37 || code >= 90 && code <= 97 {
			state.foreground = strconv.Itoa(code)
		} else if code >= 40 && code <= 47 || code >= 100 && code <= 107 {
			state.background = strconv.Itoa(code)
		}
	}
}
