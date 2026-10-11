package markdown

import (
	"strconv"
	"strings"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

type wrapStyle struct {
	bold   bool
	italic bool
	code   bool
	link   bool
}

type terminalWrapper struct {
	width            int
	requestedIndent  int
	output           strings.Builder
	row              []byte
	column           int
	lastBreakByte    int
	lastBreakColumn  int
	indent           int
	indentLocked     bool
	rowHasContent    bool
	pendingWrap      bool
	style            wrapStyle
	styleAtLastBreak wrapStyle
}

func wrapTerminal(input string, width, continuationIndent int) string {
	wrapper := terminalWrapper{
		width:           width,
		requestedIndent: continuationIndent,
		lastBreakByte:   -1,
	}
	for index := 0; index < len(input); {
		if input[index] == '\x1b' && index+1 < len(input) && input[index+1] == '[' {
			end := index + 2
			for end < len(input) && (input[end] < 0x40 || input[end] > 0x7e) {
				end++
			}
			if end < len(input) {
				wrapper.raw(input[index : end+1])
				index = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(input[index:])
		if size < 1 {
			size = 1
		}
		wrapper.rune(input[index:index+size], r)
		index += size
	}
	// An edge-space is intentionally held so a terminal never autowraps it before
	// the renderer can decide whether the next visible character starts a row.
	wrapper.pendingWrap = false
	return wrapper.output.String()
}

func (wrapper *terminalWrapper) raw(value string) {
	wrapper.updateStyle(value)
	if wrapper.pendingWrap {
		// A style transition after a held edge-space stays in source order, but
		// cannot decide whether that space should start a new row.
		wrapper.output.WriteString(value)
		return
	}
	wrapper.append(value)
}

func (wrapper *terminalWrapper) rune(value string, r rune) {
	if r == '\r' {
		return
	}
	if r == '\n' {
		wrapper.pendingWrap = false
		wrapper.output.WriteByte('\n')
		wrapper.resetRow()
		return
	}
	if value == " " && wrapper.pendingWrap {
		return
	}
	wrapper.commitPending()
	cells := terminalwidth.RuneCells(r)
	if cells == 0 {
		wrapper.append(value)
		return
	}
	budget := wrapper.rowBudget()
	isSpace := value == " "
	if isSpace && wrapper.rowHasContent && wrapper.column+cells > budget {
		wrapper.pendingWrap = true
		return
	}
	if !isSpace && wrapper.column+cells > budget && wrapper.lastBreakByte >= 0 {
		wrapper.breakAtLastSpace()
	}
	wrapper.append(value)
	wrapper.column += cells
	if isSpace {
		if wrapper.rowHasContent {
			newBreak := len(wrapper.row) - 1
			if wrapper.lastBreakByte < 0 && !wrapper.indentLocked {
				wrapper.indent = wrapper.requestedIndent
				if wrapper.indent == 0 {
					wrapper.indent = inferContinuationIndent(styledTokens(string(wrapper.row)))
				}
				wrapper.indentLocked = true
				wrapper.requestedIndent = 0
			}
			if newBreak > 0 {
				wrapper.row = append(wrapper.row[:0], wrapper.row[newBreak:]...)
				newBreak = 0
			}
			wrapper.lastBreakByte = newBreak
			wrapper.lastBreakColumn = wrapper.column
			wrapper.styleAtLastBreak = wrapper.style
		}
	} else {
		wrapper.rowHasContent = true
	}
}

func (wrapper *terminalWrapper) append(value string) {
	if wrapper.pendingWrap {
		wrapper.commitPending()
	}
	wrapper.row = append(wrapper.row, value...)
	wrapper.output.WriteString(value)
}

func (wrapper *terminalWrapper) commitPending() {
	if !wrapper.pendingWrap {
		return
	}
	wrapper.pendingWrap = false
	if wrapper.style.link {
		wrapper.output.WriteString(terminaltheme.Close(terminaltheme.Link))
	}
	wrapper.output.WriteByte('\n')
	wrapper.writeIndent(wrapper.indent)
	if wrapper.style.link {
		wrapper.output.WriteString(terminaltheme.Open(terminaltheme.Link))
	}
	indent := wrapper.indent
	wrapper.clearRow()
	wrapper.indent = indent
	wrapper.column = wrapper.indent
}

func (wrapper *terminalWrapper) breakAtLastSpace() {
	if !wrapper.indentLocked {
		wrapper.indent = wrapper.requestedIndent
		if wrapper.indent == 0 {
			wrapper.indent = inferContinuationIndent(styledTokens(string(wrapper.row)))
		}
		wrapper.indentLocked = true
		wrapper.requestedIndent = 0
	}
	eraseCells := wrapper.column - wrapper.lastBreakColumn + 1
	if eraseCells > 0 {
		wrapper.output.WriteString("\x1b[")
		wrapper.output.WriteString(strconv.Itoa(eraseCells))
		wrapper.output.WriteString("D\x1b[K")
	}
	wrapper.output.WriteByte('\n')
	wrapper.restoreBreakStyle()
	wrapper.writeIndent(wrapper.indent)
	shift := wrapper.lastBreakByte + 1
	if shift < 0 {
		shift = 0
	}
	if shift > len(wrapper.row) {
		shift = len(wrapper.row)
	}
	wrapper.output.Write(wrapper.row[shift:])
	newLength := len(wrapper.row) - shift
	copy(wrapper.row, wrapper.row[shift:])
	wrapper.row = wrapper.row[:newLength]
	wrapper.column = wrapper.indent + (wrapper.column - wrapper.lastBreakColumn)
	wrapper.lastBreakByte = -1
	wrapper.lastBreakColumn = 0
}

func (wrapper *terminalWrapper) restoreBreakStyle() {
	if wrapper.style.bold != wrapper.styleAtLastBreak.bold {
		if wrapper.styleAtLastBreak.bold {
			wrapper.output.WriteString(ansiBold)
		} else {
			wrapper.output.WriteString(ansiBoldOff)
		}
	}
	if wrapper.style.italic != wrapper.styleAtLastBreak.italic {
		if wrapper.styleAtLastBreak.italic {
			wrapper.output.WriteString(ansiItalic)
		} else {
			wrapper.output.WriteString(ansiItalicOff)
		}
	}
	if wrapper.style.code != wrapper.styleAtLastBreak.code {
		if wrapper.styleAtLastBreak.code {
			wrapper.output.WriteString(terminaltheme.Open(terminaltheme.CodeInline))
		} else {
			wrapper.output.WriteString(terminaltheme.Close(terminaltheme.CodeInline))
		}
	}
	if wrapper.style.link != wrapper.styleAtLastBreak.link {
		if wrapper.styleAtLastBreak.link {
			wrapper.output.WriteString(terminaltheme.Open(terminaltheme.Link))
		} else {
			wrapper.output.WriteString(terminaltheme.Close(terminaltheme.Link))
		}
	}
}

func (wrapper *terminalWrapper) rowBudget() int {
	if wrapper.indent >= wrapper.width {
		return wrapper.indent + 8
	}
	return wrapper.width
}

func (wrapper *terminalWrapper) writeIndent(indent int) {
	if indent > 0 {
		wrapper.output.WriteString(strings.Repeat(" ", indent))
	}
}

func (wrapper *terminalWrapper) resetRow() {
	wrapper.clearRow()
	wrapper.indent = 0
	wrapper.indentLocked = false
}

func (wrapper *terminalWrapper) clearRow() {
	wrapper.row = wrapper.row[:0]
	wrapper.column = 0
	wrapper.lastBreakByte = -1
	wrapper.lastBreakColumn = 0
	wrapper.rowHasContent = false
	wrapper.styleAtLastBreak = wrapStyle{}
}

func (wrapper *terminalWrapper) updateStyle(sequence string) {
	if len(sequence) < 3 || sequence[0] != '\x1b' || sequence[1] != '[' || sequence[len(sequence)-1] != 'm' {
		return
	}
	params := strings.Split(sequence[2:len(sequence)-1], ";")
	if len(params) == 0 || params[0] == "" {
		params = []string{"0"}
	}
	for _, parameter := range params {
		code, err := strconv.Atoi(parameter)
		if err != nil {
			continue
		}
		switch code {
		case 0:
			wrapper.style = wrapStyle{}
		case 1:
			wrapper.style.bold = true
		case 22:
			wrapper.style.bold = false
		case 3:
			wrapper.style.italic = true
		case 23:
			wrapper.style.italic = false
		case 4:
			wrapper.style.link = true
		case 24:
			wrapper.style.link = false
		case 36:
			wrapper.style.code = true
		case 39:
			wrapper.style.code = false
		}
	}
	if sequence == terminaltheme.Open(terminaltheme.CodeInline) {
		wrapper.style.code = true
	}
	if sequence == terminaltheme.Close(terminaltheme.CodeInline) {
		wrapper.style.code = false
	}
	if sequence == terminaltheme.Open(terminaltheme.Link) {
		wrapper.style.link = true
	}
	if sequence == terminaltheme.Close(terminaltheme.Link) {
		wrapper.style.link = false
	}
}
