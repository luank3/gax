package live

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

const (
	toolLineBufferCap = 4096
	toolTailBufferCap = 1500
)

type toolPreviewRow struct {
	content string
	color   string
}

// toolPreviewRenderer mirrors the settled-row behavior of hax's tool renderer. It keeps the
// newest row open so later output can settle it and finalization can replace its gutter.
type toolPreviewRenderer struct {
	mode   coretools.ToolPreviewMode
	style  coretools.ToolOutputStyle
	width  int
	styled bool

	closed        bool
	controlState  uint8
	utf8Pending   []byte
	line          []byte
	lineVisible   bool
	lineTailBytes int
	firstLine     bool
	diff          bool
	diffHunk      bool

	pending    toolPreviewRow
	hasPending bool
	rows       int

	headLines           int
	headBytes           int
	headComplete        bool
	suppressed          int
	suppressedTailBytes int
	tail                []byte
	newestTail          string
	output              strings.Builder
}

func newToolPreviewRenderer(mode coretools.ToolPreviewMode, style coretools.ToolOutputStyle, width int, styled bool) *toolPreviewRenderer {
	if width <= 0 {
		width = 100
	}
	return &toolPreviewRenderer{mode: mode, style: style, width: width, styled: styled, firstLine: true}
}

func (renderer *toolPreviewRenderer) Feed(data []byte) string {
	if renderer == nil || renderer.closed || len(data) == 0 {
		return ""
	}
	filtered := renderer.stripControls(data)
	renderer.consumeUTF8(filtered, false)
	result := renderer.output.String()
	renderer.output.Reset()
	return result
}

func (renderer *toolPreviewRenderer) Finalize() string {
	if renderer == nil || renderer.closed {
		return ""
	}
	renderer.consumeUTF8(renderer.stripControls(nil), true)
	if len(renderer.line) > 0 || renderer.lineVisible {
		renderer.processLine(string(renderer.line), renderer.lineVisible)
		renderer.line = renderer.line[:0]
		renderer.lineVisible = false
	}

	var suffix []string
	if renderer.headComplete {
		switch renderer.mode {
		case coretools.PreviewHead:
			if renderer.suppressed > 0 {
				suffix = append(suffix, "... ("+intString(renderer.suppressed)+" more line"+plural(renderer.suppressed)+")")
			}
		case coretools.PreviewHeadTail:
			tailRows := renderer.tailRows()
			elided := renderer.suppressed - len(tailRows)
			if elided > 0 {
				suffix = append(suffix, "... ("+intString(elided)+" more line"+plural(elided)+") ...")
			}
			suffix = append(suffix, tailRows...)
		}
	}

	if len(suffix) > 0 {
		if renderer.hasPending {
			renderer.settlePending()
		}
		for _, content := range suffix[:len(suffix)-1] {
			renderer.emitRow(toolPreviewRow{content: renderer.truncate(content), color: ansiDim}, false)
		}
		last := suffix[len(suffix)-1]
		renderer.emitRow(toolPreviewRow{content: renderer.truncate(last), color: ansiDim}, true)
	} else if renderer.hasPending {
		renderer.emitRow(renderer.pending, true)
		renderer.hasPending = false
	} else if renderer.rows > 0 {
		renderer.closeLastRow()
	}

	renderer.closed = true
	result := renderer.output.String()
	renderer.output.Reset()
	return result
}

func (renderer *toolPreviewRenderer) stripControls(data []byte) []byte {
	output := make([]byte, 0, len(data))
	for index := 0; index < len(data); {
		byteValue := data[index]
		index++
		reprocess := true
		for reprocess {
			reprocess = false
			switch renderer.controlState {
			case 0: // text
				if byteValue == 0x1b {
					renderer.controlState = 1
				} else if byteValue == '\t' || byteValue == '\n' || byteValue >= 0x20 && byteValue != 0x7f {
					output = append(output, byteValue)
				}
			case 1: // escape
				switch {
				case byteValue == '[':
					renderer.controlState = 2
				case byteValue == ']':
					renderer.controlState = 3
				case byteValue == 'P' || byteValue == '^' || byteValue == '_':
					renderer.controlState = 5
				case byteValue >= 0x20 && byteValue <= 0x2f:
					renderer.controlState = 7
				case byteValue >= 0x30 && byteValue <= 0x7e:
					renderer.controlState = 0
				default:
					renderer.controlState = 0
					reprocess = true
				}
			case 2: // CSI
				if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else if byteValue >= 0x40 && byteValue <= 0x7e {
					renderer.controlState = 0
				}
			case 3: // OSC
				if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else if byteValue == 0x07 {
					renderer.controlState = 0
				} else if byteValue == 0x1b {
					renderer.controlState = 4
				}
			case 4: // OSC after ESC
				if byteValue == '\\' {
					renderer.controlState = 0
				} else if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else {
					renderer.controlState = 3
					reprocess = true
				}
			case 5: // DCS, SOS, PM, or APC string
				if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else if byteValue == 0x1b {
					renderer.controlState = 6
				}
			case 6: // control string after ESC
				if byteValue == '\\' {
					renderer.controlState = 0
				} else if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else {
					renderer.controlState = 5
					reprocess = true
				}
			case 7: // escape intermediate
				if byteValue == '\n' || byteValue == 0x18 || byteValue == 0x1a {
					renderer.controlState = 0
					reprocess = true
				} else if byteValue >= 0x30 && byteValue <= 0x7e {
					renderer.controlState = 0
				}
			}
		}
	}
	return output
}

func (renderer *toolPreviewRenderer) consumeUTF8(data []byte, final bool) {
	input := append([]byte(nil), renderer.utf8Pending...)
	input = append(input, data...)
	renderer.utf8Pending = renderer.utf8Pending[:0]
	for offset := 0; offset < len(input); {
		if !final && !utf8.FullRune(input[offset:]) {
			renderer.utf8Pending = append(renderer.utf8Pending, input[offset:]...)
			break
		}
		runeValue, size := utf8.DecodeRune(input[offset:])
		if size == 0 {
			break
		}
		if runeValue == utf8.RuneError && size == 1 && input[offset] >= utf8.RuneSelf || runeValue == 0 {
			runeValue = utf8.RuneError
		}
		if runeValue == '\r' {
			// The upstream control stripper drops carriage returns before UTF-8 decoding.
			offset += size
			continue
		}
		if runeValue == '\n' {
			renderer.pushTail([]byte{'\n'})
			renderer.processLine(string(renderer.line), renderer.lineVisible)
			renderer.line = renderer.line[:0]
			renderer.lineVisible = false
			renderer.lineTailBytes = 0
			offset += size
			continue
		}
		if runeValue == '\t' {
			renderer.pushTail([]byte("    "))
			for index := 0; index < 4; index++ {
				if len(renderer.line)+1 <= toolLineBufferCap {
					renderer.line = append(renderer.line, ' ')
				}
			}
			offset += size
			continue
		}
		if unsafeToolRune(runeValue) {
			runeValue = '?'
		}
		encoded := string(runeValue)
		if len(renderer.line)+len(encoded) <= toolLineBufferCap {
			renderer.line = append(renderer.line, encoded...)
		}
		renderer.pushTail([]byte(encoded))
		if runeValue != ' ' {
			renderer.lineVisible = true
		}
		offset += size
	}
}

func unsafeToolRune(value rune) bool {
	if unicode.IsControl(value) {
		return true
	}
	return value == 0x00ad || value == 0x034f || value == 0x061c || value == 0x115f || value == 0x1160 ||
		value == 0x180e || value >= 0x200b && value <= 0x200f || value >= 0x2028 && value <= 0x2029 ||
		value >= 0x202a && value <= 0x202e || value >= 0x2060 && value <= 0x206f || value == 0x3164 ||
		value == 0xfeff || value == 0xffa0 || value >= 0xfff9 && value <= 0xfffb ||
		value >= 0xe0000 && value <= 0xe007f
}

func (renderer *toolPreviewRenderer) processLine(line string, visible bool) {
	if renderer.firstLine {
		renderer.firstLine = false
		renderer.diff = renderer.style == coretools.OutputUnifiedDiff && strings.HasPrefix(line, "--- ")
	}
	if renderer.diff {
		renderer.processDiffLine(line)
		return
	}
	if renderer.headComplete {
		renderer.appendTail(line, visible)
		return
	}
	if !visible {
		if renderer.hasPending && renderer.wouldReachHeadLimit(renderer.pending.content) {
			renderer.settlePending()
			renderer.markHeadComplete()
			renderer.appendTail(line, false)
		}
		return
	}
	if renderer.hasPending {
		renderer.settlePending()
		if renderer.hasReachedHeadLimit() {
			renderer.markHeadComplete()
			renderer.appendTail(line, true)
			return
		}
	}
	renderer.pending = toolPreviewRow{content: renderer.truncate(line), color: ansiDim}
	renderer.hasPending = true
}

func (renderer *toolPreviewRenderer) wouldReachHeadLimit(content string) bool {
	maxLines, maxBytes := 8, 3000
	if renderer.mode == coretools.PreviewHeadTail {
		maxLines, maxBytes = 4, 1500
	}
	return renderer.headLines+1 >= maxLines || renderer.headBytes+len(content)+1 >= maxBytes
}

func (renderer *toolPreviewRenderer) hasReachedHeadLimit() bool {
	maxLines, maxBytes := 8, 3000
	if renderer.mode == coretools.PreviewHeadTail {
		maxLines, maxBytes = 4, 1500
	}
	return renderer.headLines >= maxLines || renderer.headBytes >= maxBytes
}

func (renderer *toolPreviewRenderer) settlePending() {
	if !renderer.hasPending {
		return
	}
	content := renderer.pending.content
	renderer.emitRow(renderer.pending, false)
	renderer.headLines++
	renderer.headBytes += len(content) + 1
	renderer.hasPending = false
}

func (renderer *toolPreviewRenderer) processDiffLine(line string) {
	if !renderer.diffHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
		return
	}
	color := ansiDim
	switch {
	case strings.HasPrefix(line, "\\"), strings.HasPrefix(line, "@@"):
		color = ansiDim
	case renderer.diffHunk && strings.HasPrefix(line, "+"):
		color = terminaltheme.Open(terminaltheme.Add)
	case renderer.diffHunk && strings.HasPrefix(line, "-"):
		color = terminaltheme.Open(terminaltheme.Remove)
	case !renderer.diffHunk && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
		color = terminaltheme.Open(terminaltheme.Add)
	case !renderer.diffHunk && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
		color = terminaltheme.Open(terminaltheme.Remove)
	}
	if !renderer.styled {
		color = ""
	}
	if renderer.hasPending {
		renderer.settleDisplayPending()
	}
	renderer.pending = toolPreviewRow{content: renderer.truncate(line), color: color}
	renderer.hasPending = true
	if strings.HasPrefix(line, "@@") {
		renderer.diffHunk = true
	}
}

func (renderer *toolPreviewRenderer) settleDisplayPending() {
	if !renderer.hasPending {
		return
	}
	renderer.emitRow(renderer.pending, false)
	renderer.hasPending = false
}

func (renderer *toolPreviewRenderer) appendTail(line string, visible bool) {
	if visible {
		renderer.suppressed++
		renderer.newestTail = renderer.truncate(line)
	}
}

func (renderer *toolPreviewRenderer) tailRows() []string {
	rows := make([]string, 0, 4)
	visibleBytes := renderer.suppressedTailBytes
	if visibleBytes > len(renderer.tail) {
		visibleBytes = len(renderer.tail)
	}
	tail := renderer.tail[len(renderer.tail)-visibleBytes:]
	for len(tail) > 0 && tail[0]&0xc0 == 0x80 {
		tail = tail[1:]
	}
	for _, line := range strings.Split(string(tail), "\n") {
		if strings.Trim(line, " \t") != "" {
			rows = append(rows, renderer.truncate(line))
		}
	}
	if len(rows) > 4 {
		rows = rows[len(rows)-4:]
	}
	if renderer.suppressed > 0 && len(rows) > 0 && renderer.newestTail != "" {
		rows[len(rows)-1] = renderer.newestTail
	}
	return rows
}

func (renderer *toolPreviewRenderer) pushTail(data []byte) {
	if renderer.mode != coretools.PreviewHeadTail || len(data) == 0 {
		return
	}
	if renderer.headComplete {
		renderer.suppressedTailBytes += len(data)
	}
	renderer.lineTailBytes += len(data)
	if len(data) >= toolTailBufferCap {
		renderer.tail = append(renderer.tail[:0], data[len(data)-toolTailBufferCap:]...)
		return
	}
	if extra := len(renderer.tail) + len(data) - toolTailBufferCap; extra > 0 {
		copy(renderer.tail, renderer.tail[extra:])
		renderer.tail = renderer.tail[:len(renderer.tail)-extra]
	}
	renderer.tail = append(renderer.tail, data...)
}

func (renderer *toolPreviewRenderer) markHeadComplete() {
	if renderer.headComplete {
		return
	}
	renderer.headComplete = true
	renderer.suppressedTailBytes += renderer.lineTailBytes
}

func (renderer *toolPreviewRenderer) emitRow(row toolPreviewRow, final bool) {
	if renderer.rows > 0 {
		renderer.output.WriteByte('\n')
	}
	gutter := "┌"
	if renderer.rows > 0 {
		gutter = "│"
	}
	renderer.writeGutter(gutter)
	renderer.writeContent(row)
	renderer.rows++
	if final {
		renderer.closeLastRow()
	}
}

func (renderer *toolPreviewRenderer) writeGutter(glyph string) {
	if renderer.styled {
		renderer.output.WriteString(terminaltheme.Open(terminaltheme.ChromeDim))
	}
	renderer.output.WriteString(glyph)
	renderer.output.WriteByte(' ')
	if renderer.styled {
		renderer.output.WriteString(ansiReset)
	}
}

func (renderer *toolPreviewRenderer) writeContent(row toolPreviewRow) {
	if renderer.styled && row.color != "" {
		renderer.output.WriteString(row.color)
	}
	renderer.output.WriteString(row.content)
	if renderer.styled && row.color != "" {
		renderer.output.WriteString(ansiReset)
	}
}

func (renderer *toolPreviewRenderer) closeLastRow() {
	if renderer.rows == 0 {
		return
	}
	glyph := "└"
	if renderer.rows == 1 {
		glyph = "›"
	}
	renderer.output.WriteByte('\r')
	if renderer.styled {
		renderer.output.WriteString(ansiDim)
	}
	renderer.output.WriteString(glyph)
	if renderer.styled {
		renderer.output.WriteString(ansiReset)
	}
	renderer.output.WriteByte('\n')
}

func (renderer *toolPreviewRenderer) NoChangesMarker() string {
	var output strings.Builder
	if renderer.styled {
		output.WriteString(ansiDim)
	}
	output.WriteString("› ")
	if renderer.styled {
		output.WriteString(ansiReset + ansiDim)
	}
	output.WriteString("(no changes)")
	if renderer.styled {
		output.WriteString(ansiReset)
	}
	output.WriteByte('\n')
	return output.String()
}

func (renderer *toolPreviewRenderer) truncate(value string) string {
	budget := renderer.width - 3
	if renderer.width <= 7 {
		budget = 1
	}
	if budget < 1 || terminalwidth.Cells(value) <= budget {
		return value
	}
	contentBudget := budget
	ellipsis := ""
	if budget >= 4 {
		contentBudget -= 3
		ellipsis = "..."
	}
	var output strings.Builder
	cells := 0
	for _, runeValue := range value {
		cellWidth := terminalwidth.RuneCells(runeValue)
		if cells+cellWidth > contentBudget {
			break
		}
		output.WriteRune(runeValue)
		cells += cellWidth
	}
	output.WriteString(ellipsis)
	return output.String()
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func intString(value int) string {
	return strconv.Itoa(value)
}
