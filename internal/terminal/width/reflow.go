package width

import (
	"strings"
	"unicode/utf8"
)

const maxReflowSlackCells = 16

// ReflowForDisplay reflows a display argument across bounded rows, leaving suffixReserve cells
// free on every row so a tool-provided suffix can be appended without wrapping.
func ReflowForDisplay(text string, firstRowCells, otherRowCells, maxRows, suffixReserve int) string {
	if maxRows < 1 {
		maxRows = 1
	}
	if firstRowCells < 1 {
		firstRowCells = 1
	}
	if otherRowCells < 1 {
		otherRowCells = 1
	}
	if suffixReserve < 0 {
		suffixReserve = 0
	}
	singleRowCells := firstRowCells - suffixReserve
	if singleRowCells < 1 {
		singleRowCells = 1
	}
	if len(text) <= singleRowCells {
		return text
	}

	var result strings.Builder
	offset := 0
	for row := 0; row < maxRows && offset < len(text); row++ {
		rowCells := otherRowCells
		if row == 0 {
			rowCells = firstRowCells
		}
		contentCells := rowCells - suffixReserve
		if contentCells < 1 {
			contentCells = 1
		}
		remaining := text[offset:]
		if advanceDisplayCells(remaining, contentCells) == len(remaining) {
			result.WriteString(remaining)
			break
		}
		if row == maxRows-1 {
			beforeEllipsis := contentCells - 3
			if beforeEllipsis < 1 {
				result.WriteString(remaining[:advanceDisplayCells(remaining, contentCells)])
				break
			}
			rowBytes, _ := reflowBreakPosition(remaining, beforeEllipsis)
			result.WriteString(remaining[:rowBytes])
			result.WriteString("...")
			break
		}

		rowBytes, nextOffset := reflowBreakPosition(remaining, contentCells)
		if nextOffset == 0 {
			rowBytes = firstDisplayCodepointEnd(remaining)
			nextOffset = rowBytes
		}
		result.WriteString(remaining[:rowBytes])
		result.WriteByte('\n')
		offset += nextOffset
	}
	return result.String()
}

func advanceDisplayCells(text string, maxCells int) int {
	offset, cells := 0, 0
	for offset < len(text) {
		runeValue, size := utf8.DecodeRuneInString(text[offset:])
		width := RuneCells(runeValue)
		if cells >= maxCells || cells+width > maxCells {
			break
		}
		cells += width
		offset += size
	}
	for offset < len(text) {
		runeValue, size := utf8.DecodeRuneInString(text[offset:])
		if RuneCells(runeValue) != 0 {
			break
		}
		offset += size
	}
	return offset
}

func reflowBreakPosition(text string, maxCells int) (rowEnd, nextOffset int) {
	cells := 0
	spaceOffset, spaceCells := -1, 0
	markBreakOffset, markBreakCells := -1, 0
	afterMark := false
	offset := 0
	for offset < len(text) {
		runeValue, size := utf8.DecodeRuneInString(text[offset:])
		width := RuneCells(runeValue)
		if runeValue == ' ' {
			spaceOffset = offset
			spaceCells = cells
		} else if afterMark && width > 0 && !isReflowBreakMark(runeValue) {
			markBreakOffset = offset
			markBreakCells = cells
		}
		if width > 0 {
			afterMark = isReflowBreakMark(runeValue)
		}
		if cells+width > maxCells {
			break
		}
		cells += width
		offset += size
	}
	if offset >= len(text) {
		return len(text), len(text)
	}
	maxSlack := maxCells / 2
	if maxSlack > maxReflowSlackCells {
		maxSlack = maxReflowSlackCells
	}
	if spaceOffset >= 0 && maxCells-spaceCells <= maxSlack {
		rowEnd = spaceOffset
		for rowEnd > 0 && text[rowEnd-1] == ' ' {
			rowEnd--
		}
		return rowEnd, spaceOffset + 1
	}
	if markBreakOffset >= 0 && maxCells-markBreakCells <= maxSlack {
		return markBreakOffset, markBreakOffset
	}
	return offset, offset
}

func firstDisplayCodepointEnd(text string) int {
	if text == "" {
		return 0
	}
	runeValue, size := utf8.DecodeRuneInString(text)
	if size == 0 {
		return 0
	}
	offset := size
	for offset < len(text) {
		runeValue, size = utf8.DecodeRuneInString(text[offset:])
		if RuneCells(runeValue) != 0 {
			break
		}
		offset += size
	}
	return offset
}

func isReflowBreakMark(runeValue rune) bool {
	switch runeValue {
	case '|', '/', ',', ';', '&', '=':
		return true
	default:
		return false
	}
}
