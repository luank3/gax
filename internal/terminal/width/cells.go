package width

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Cells reports the number of terminal cells occupied by text.
func Cells(text string) int {
	cells := 0
	for _, value := range text {
		cells += RuneCells(value)
	}
	return cells
}

// RuneCells reports the terminal-cell width of one rune using the editor's wcwidth-compatible
// ranges.
func RuneCells(value rune) int {
	if value < 0x20 || value == 0x7f || unicode.IsControl(value) {
		return 1
	}
	if unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) || value == 0x200d {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		value >= 0x2e80 && value <= 0xa4cf || value >= 0xac00 && value <= 0xd7a3 ||
		value >= 0xf900 && value <= 0xfaff || value >= 0xfe10 && value <= 0xfe19 ||
		value >= 0xfe30 && value <= 0xfe6f || value >= 0xff00 && value <= 0xff60 ||
		value >= 0xffe0 && value <= 0xffe6 || value >= 0x1f300 && value <= 0x1faff ||
		value >= 0x20000 && value <= 0x3fffd) {
		return 2
	}
	return 1
}

// WrapRows breaks text at spaces where possible and hard-wraps oversized words. Continuation rows
// are prefixed with indent spaces; embedded newlines create explicit rows.
func WrapRows(text string, indent, columns int) []string {
	if columns < 1 {
		columns = 1
	}
	if indent < 0 {
		indent = 0
	}
	budget := columns - indent
	if budget < 1 {
		budget = 1
	}
	rows := make([]string, 0, 1)
	first := true
	for text != "" || first {
		row, remaining := wrapRow(text, budget)
		if !first {
			row = spaces(indent) + row
		}
		rows = append(rows, row)
		text = remaining
		first = false
	}
	return rows
}

func wrapRow(text string, budget int) (row, remaining string) {
	if text == "" {
		return "", ""
	}
	newline := -1
	for index, value := range text {
		if value == '\n' {
			newline = index
			break
		}
	}
	line := text
	if newline >= 0 {
		line = text[:newline]
	}
	if line == "" {
		if newline >= 0 {
			return "", text[newline+1:]
		}
		return "", ""
	}
	cells, lastSpace, fitBytes := 0, -1, 0
	for index := 0; index < len(line); {
		value, size := utf8.DecodeRuneInString(line[index:])
		cellWidth := RuneCells(value)
		if cells+cellWidth > budget {
			if value == ' ' && cells == budget {
				lastSpace = index
			}
			if lastSpace >= 0 {
				end := lastSpace
				for end > 0 && line[end-1] == ' ' {
					end--
				}
				return line[:end], text[lastSpace+1:]
			}
			if fitBytes == 0 {
				fitBytes = index + size
			}
			return line[:fitBytes], text[fitBytes:]
		}
		if value == ' ' {
			lastSpace = index
		}
		cells += cellWidth
		index += size
		fitBytes = index
	}
	if newline >= 0 {
		return line, text[newline+1:]
	}
	return line, ""
}

func spaces(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat(" ", count)
}
