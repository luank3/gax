package markdown

import (
	"strings"
	"unicode/utf8"

	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	inlineLineBoundary = '\x00'
	// inlineLiteralTail marks bytes the parser resolved as literal at end of input: the walker
	// copies them verbatim, which keeps them inside whatever run is still open, as upstream's
	// newline handler does before md_flush closes it.
	inlineLiteralTail = '\x01'
	// emDash is the three-byte clause dash the renderer spaces against its neighbours.
	emDash           = "\u2014"
	ansiBold         = "\x1b[1m"
	ansiDim          = "\x1b[2m"
	ansiItalic       = "\x1b[3m"
	ansiBoldOff      = "\x1b[22m"
	ansiItalicOff    = "\x1b[23m"
	ansiCyan         = "\x1b[36m"
	ansiFGDefault    = "\x1b[39m"
	ansiUnderline    = "\x1b[4m"
	ansiUnderlineOff = "\x1b[24m"
	maxTableCols     = 32
	maxTableRows     = 2048
	maxTableBytes    = 1 << 16
)

// HasTrailingNewline reports whether output ends after a visible line break, ignoring ANSI CSI
// sequences which may follow that line break to restore terminal style.
func HasTrailingNewline(input string) bool {
	return strings.HasSuffix(stripANSI(input), "\n")
}

func inlineCodeOpenAtEnd(line string) bool {
	inCode := false
	for index := 0; index < len(line); index++ {
		if line[index] == '`' {
			inCode = !inCode
		}
	}
	return inCode
}

func renderInline(input string, styled bool) string {
	return renderInlineWithOptions(input, styled, false)
}

func renderInlineWithOptions(input string, styled, suppressBold bool) string {
	text, _ := renderInlineWithState(input, styled, suppressBold, false)
	return text
}

func renderInlineWithDeferredItalicClose(input string, styled bool) (string, bool) {
	return renderInlineWithState(input, styled, false, true)
}

type inlineState struct {
	// prevByte is the last byte consumed from the document, so emphasis rules still see their left
	// neighbour when a chunk boundary falls between the marker and that byte.
	prevByte              byte
	styled                bool
	suppressBold          bool
	pendingDash           bool
	previousSpace         bool
	hasPrevious           bool
	unmatchedItalic       bool
	deferFinalItalicClose bool
	leftItalicOpen        bool
}

// renderInlineWithState renders one complete inline segment. It is the final=true entry point of
// the incremental walker, so streaming and whole-document rendering share one implementation and
// cannot disagree.
func renderInlineWithState(input string, styled, suppressBold, deferItalicClose bool) (string, bool) {
	var out strings.Builder
	state := inlineState{
		styled: styled, suppressBold: suppressBold, deferFinalItalicClose: deferItalicClose,
	}
	_, _ = state.renderResumable(func(chunk string) error {
		out.WriteString(chunk)
		return nil
	}, input, true)
	return out.String(), state.leftItalicOpen
}

func isMarkdownSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func bareURL(input string, index int) (int, bool) {
	if index > 0 && isASCIIAlnum(input[index-1]) {
		return index, false
	}
	// Only the scheme decides whether a URL starts here. Lowercasing the whole remainder is O(n)
	// per position, which makes the inline pass quadratic on long responses; compare the bounded
	// scheme slice instead.
	schemeLength := 0
	const httpsScheme, httpScheme = "https://", "http://"
	remaining := input[index:]
	if len(remaining) >= len(httpsScheme) && strings.EqualFold(remaining[:len(httpsScheme)], httpsScheme) {
		schemeLength = len(httpsScheme)
	} else if len(remaining) >= len(httpScheme) && strings.EqualFold(remaining[:len(httpScheme)], httpScheme) {
		schemeLength = len(httpScheme)
	} else {
		return index, false
	}
	start := index + schemeLength
	end := start
	for end < len(input) && input[end] != ' ' && input[end] != '\t' && input[end] != '\n' &&
		input[end] != '\r' && input[end] != '<' && input[end] != '>' && input[end] != '`' &&
		input[end] != inlineLineBoundary {
		end++
	}
	if start == end {
		return index, false
	}
	end = trimLinkEnd(input, start, end)
	return end, end > start
}

func trimLinkEnd(input string, start, end int) int {
	for end > start {
		last := input[end-1]
		if strings.ContainsRune(".,;:!?*_\"", rune(last)) {
			end--
			continue
		}
		var opener byte
		switch last {
		case ')':
			opener = '('
		case ']':
			opener = '['
		case '}':
			opener = '{'
		}
		if opener != 0 {
			depth := 0
			for index := start; index < end-1; index++ {
				switch input[index] {
				case opener:
					depth++
				case last:
					depth--
				}
			}
			if depth <= 0 {
				end--
				continue
			}
		}
		break
	}
	return end
}

func isASCIIAlnum(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func canOpenEmphasis(left, right byte) bool {
	isAlnum := func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
	}
	isSpace := func(value byte) bool {
		return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == inlineLineBoundary
	}
	return !isAlnum(left) && !isSpace(right)
}

func findItalicCloser(input string) int {
	inCode := false
	for index := 0; index < len(input); index++ {
		if input[index] == inlineLineBoundary {
			inCode = false
			continue
		}
		if input[index] == '`' {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if end, ok := bareURL(input, index); ok {
			index = end - 1
			continue
		}
		if input[index] == '_' {
			return index
		}
		if input[index] != '*' || index > 0 && input[index-1] == '*' ||
			index+1 < len(input) && input[index+1] == '*' {
			continue
		}
		return index
	}
	return -1
}

func isFenceCloser(line string, fenceLength int) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	start := index
	for index < len(line) && line[index] == '`' {
		index++
	}
	if index-start < fenceLength {
		return false
	}
	return strings.TrimSpace(line[index:]) == ""
}

func isIncompleteFenceCandidate(line string) bool {
	info := ScanLine(line, false)
	return info.Kind == LineFence && !info.Complete
}

type styledRune struct {
	text  string
	cells int
	space bool
}

func wrapStyled(input string, width, continuationIndent int) string {
	tabReplacement := "    "
	if width > 0 {
		tabReplacement = " "
	}
	input = strings.ReplaceAll(input, "\t", tabReplacement)
	if width <= 0 {
		return input
	}
	return wrapTerminal(input, width, continuationIndent)
}

func inferContinuationIndent(tokens []styledRune) int {
	index := skipStyledTokens(tokens, 0)
	leadingSpaces := 0
	for index < len(tokens) && tokens[index].text == " " && leadingSpaces < 8 {
		leadingSpaces++
		index++
	}
	index = skipStyledTokens(tokens, index)
	if index >= len(tokens) {
		return leadingSpaces
	}
	if tokens[index].text == "•" && index+1 < len(tokens) && tokens[index+1].text == " " {
		return leadingSpaces + 2
	}
	if (tokens[index].text == "*" || tokens[index].text == "-" || tokens[index].text == "+") &&
		index+1 < len(tokens) && tokens[index+1].text == " " {
		return leadingSpaces + 2
	}
	if len(tokens[index].text) == 1 && tokens[index].text[0] >= '0' && tokens[index].text[0] <= '9' {
		end := index + 1
		for end < len(tokens) && len(tokens[end].text) == 1 && tokens[end].text[0] >= '0' && tokens[end].text[0] <= '9' {
			end++
		}
		if end+1 < len(tokens) && (tokens[end].text == "." || tokens[end].text == ")") && tokens[end+1].text == " " {
			return leadingSpaces + end - index + 2
		}
	}
	return leadingSpaces
}

func skipStyledTokens(tokens []styledRune, index int) int {
	for index < len(tokens) && strings.HasPrefix(tokens[index].text, "\x1b[") {
		index++
	}
	return index
}

func styledTokens(line string) []styledRune {
	tokens := make([]styledRune, 0, utf8.RuneCountInString(line))
	for index := 0; index < len(line); {
		if line[index] == '\x1b' && index+1 < len(line) && line[index+1] == '[' {
			end := index + 2
			for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
				end++
			}
			if end < len(line) {
				end++
				tokens = append(tokens, styledRune{text: line[index:end]})
				index = end
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[index:])
		text := line[index : index+size]
		tokens = append(tokens, styledRune{text: text, cells: terminalwidth.RuneCells(r), space: r == ' '})
		index += size
	}
	return tokens
}

func stripANSI(input string) string {
	var output strings.Builder
	for index := 0; index < len(input); {
		if input[index] == '\x1b' && index+1 < len(input) && input[index+1] == '[' {
			end := index + 2
			for end < len(input) && (input[end] < 0x40 || input[end] > 0x7e) {
				end++
			}
			if end < len(input) {
				index = end + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(input[index:])
		output.WriteString(input[index : index+size])
		index += size
	}
	return output.String()
}

func parseTable(lines []string, start int, finalHasNewline bool) ([][]string, []byte, int, bool) {
	if start+1 >= len(lines) {
		return nil, nil, start, false
	}
	header := splitTableRow(lines[start])
	delimiter := splitTableRow(lines[start+1])
	if len(header) == 0 || len(header) > maxTableCols || len(header) != len(delimiter) {
		return nil, nil, start, false
	}
	bufferedBytes := len(lines[start]) + len(lines[start+1]) + 2
	if bufferedBytes > maxTableBytes {
		return nil, nil, start, false
	}
	alignments := make([]byte, len(delimiter))
	for index, cell := range delimiter {
		if len(cell) == 0 {
			return nil, nil, start, false
		}
		leftColon, rightColon := cell[0] == ':', cell[len(cell)-1] == ':'
		dashStart, dashEnd := 0, len(cell)
		if leftColon {
			dashStart++
		}
		if rightColon {
			dashEnd--
		}
		if dashStart >= dashEnd {
			return nil, nil, start, false
		}
		for _, marker := range cell[dashStart:dashEnd] {
			if marker != '-' {
				return nil, nil, start, false
			}
		}
		alignments[index] = 'L'
		if leftColon && rightColon {
			alignments[index] = 'C'
		} else if rightColon {
			alignments[index] = 'R'
		}
	}
	rows := [][]string{header}
	end := start + 1
	tableLineCount := 2
	fallback := false
	for index := start + 2; index < len(lines); index++ {
		if !strings.Contains(lines[index], "|") {
			break
		}
		row := splitTableRow(lines[index])
		if len(row) == 0 {
			break
		}
		if bufferedBytes+len(lines[index])+1 > maxTableBytes {
			if index == len(lines)-1 && !finalHasNewline {
				// An oversized unterminated tail is still part of the active table
				// attempt; the upstream streaming parser bails the whole attempt
				// through its lossless literal path.
				return nil, nil, index, true
			}
			break
		}
		bufferedBytes += len(lines[index]) + 1
		tableLineCount++
		end = index
		if len(row) > len(header) || tableLineCount > maxTableRows {
			fallback = true
			rows = nil
		} else if !fallback {
			rows = append(rows, row)
		}
	}
	if fallback {
		return nil, nil, end, true
	}
	return rows, alignments, end, true
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return nil
	}
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

func renderTable(rows [][]string, alignments []byte, width int, styled bool) string {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return ""
	}
	columnCount := len(rows[0])
	widths := make([]int, columnCount)
	for _, row := range rows {
		for column := 0; column < columnCount && column < len(row); column++ {
			visible := terminalwidth.Cells(stripANSI(renderInline(row[column], false)))
			if visible > widths[column] {
				widths[column] = visible
			}
		}
	}
	for column := range widths {
		if widths[column] == 0 {
			widths[column] = 1
		}
	}
	gridWidth := 3 * (columnCount - 1)
	for _, columnWidth := range widths {
		gridWidth += columnWidth
	}
	if width > 0 && gridWidth > width && len(rows) > 1 {
		return renderReflowedTable(rows, width, styled)
	}
	var output strings.Builder
	for rowIndex, row := range rows {
		for column := 0; column < columnCount; column++ {
			cell := ""
			if column < len(row) {
				cell = strings.ReplaceAll(row[column], "\t", "    ")
			}
			text := renderInline(cell, styled)
			if rowIndex == 0 {
				if styled {
					text = ansiBold + renderInlineWithOptions(cell, true, true) + ansiBoldOff
				} else {
					text = renderInlineWithOptions(cell, false, true)
				}
			}
			padding := widths[column] - terminalwidth.Cells(stripANSI(text))
			alignment := byte('L')
			if column < len(alignments) {
				alignment = alignments[column]
			}
			leftPadding, rightPadding := 0, padding
			if alignment == 'R' {
				leftPadding, rightPadding = padding, 0
			} else if alignment == 'C' {
				leftPadding = padding / 2
				rightPadding = padding - leftPadding
			}
			if leftPadding > 0 {
				output.WriteString(strings.Repeat(" ", leftPadding))
			}
			output.WriteString(text)
			if column+1 < columnCount && rightPadding > 0 {
				output.WriteString(strings.Repeat(" ", rightPadding))
			}
			if column+1 < columnCount {
				output.WriteByte(' ')
				if styled {
					output.WriteString(ansiDim)
				}
				output.WriteString("│")
				if styled {
					output.WriteString(ansiBoldOff)
				}
				output.WriteByte(' ')
			}
		}
		output.WriteByte('\n')
		if rowIndex == 0 {
			if styled {
				output.WriteString(ansiDim)
			}
			for column, width := range widths {
				output.WriteString(strings.Repeat("─", width))
				if column+1 < columnCount {
					// The crossing rule includes dashes under both spaces around │.
					output.WriteString("─┼─")
				}
			}
			if styled {
				output.WriteString(ansiBoldOff)
			}
			output.WriteByte('\n')
		}
	}
	return output.String()
}

func renderReflowedTable(rows [][]string, width int, styled bool) string {
	var output strings.Builder
	bullet := "• "
	if styled {
		bullet = ansiDim + bullet + ansiBoldOff
	}
	for _, row := range rows[1:] {
		for column, label := range rows[0] {
			value := ""
			if column < len(row) {
				value = row[column]
			}
			prefix := "  "
			if column == 0 {
				prefix = bullet
			}
			formattedLabel := renderInlineWithOptions(label, styled, true)
			if styled {
				formattedLabel = ansiBold + formattedLabel + ansiBoldOff
			}
			line := wrapStyled(prefix+formattedLabel+": "+renderInline(value, styled), width, 2)
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
	return output.String()
}
