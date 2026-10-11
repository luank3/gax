package width

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FlattenForDisplay makes untrusted text a safe, single-line terminal label and bounds runs of
// zero-width codepoints so they cannot hide or distort neighboring text.
func FlattenForDisplay(text string) string {
	var output strings.Builder
	previousWasSpace := true
	zeroWidthRun := 0
	for offset := 0; offset < len(text); {
		byteValue := text[offset]
		if byteValue < utf8.RuneSelf {
			if byteValue == ' ' || byteValue == '\t' || byteValue == '\n' || byteValue == '\r' || byteValue < 0x20 || byteValue == 0x7f {
				if !previousWasSpace {
					output.WriteByte(' ')
					previousWasSpace = true
				}
			} else {
				output.WriteByte(byteValue)
				previousWasSpace = false
			}
			zeroWidthRun = 0
			offset++
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(text[offset:])
		if runeValue == utf8.RuneError && size == 1 || unsafeDisplayRune(runeValue) {
			output.WriteByte('?')
			zeroWidthRun = 0
			previousWasSpace = false
		} else if cells := RuneCells(runeValue); cells == 0 {
			if zeroWidthRun < 8 {
				output.WriteString(text[offset : offset+size])
				zeroWidthRun++
			}
		} else {
			output.WriteString(text[offset : offset+size])
			zeroWidthRun = 0
			previousWasSpace = false
		}
		offset += size
	}
	result := output.String()
	return strings.TrimSuffix(result, " ")
}

// TruncateForDisplay clips text at a display-cell boundary and uses three ASCII dots when at
// least four cells are available.
func TruncateForDisplay(text string, maxCells int) string {
	if maxCells < 0 {
		maxCells = 0
	}
	if len(text) <= maxCells || advanceDisplayCells(text, maxCells) == len(text) {
		return text
	}
	contentCells := maxCells
	ellipsis := ""
	if maxCells >= 4 {
		contentCells -= 3
		ellipsis = "..."
	}
	return text[:advanceDisplayCells(text, contentCells)] + ellipsis
}

func unsafeDisplayRune(value rune) bool {
	if unicode.IsControl(value) {
		return true
	}
	return value == 0x00ad || value == 0x034f || value == 0x061c || value == 0x115f || value == 0x1160 ||
		value == 0x180e || value >= 0x200b && value <= 0x200f || value >= 0x2028 && value <= 0x2029 ||
		value >= 0x202a && value <= 0x202e || value >= 0x2060 && value <= 0x206f || value == 0x3164 ||
		value == 0xfeff || value == 0xffa0 || value >= 0xfff9 && value <= 0xfffb ||
		value >= 0xe0000 && value <= 0xe007f
}
