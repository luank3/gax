package markdown

import (
	"strings"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	inlineLineBoundary = '\x00'
	ansiBold           = "\x1b[1m"
	ansiDim            = "\x1b[2m"
	ansiItalic         = "\x1b[3m"
	ansiBoldOff        = "\x1b[22m"
	ansiItalicOff      = "\x1b[23m"
	ansiCyan           = "\x1b[36m"
	ansiFGDefault      = "\x1b[39m"
	ansiUnderline      = "\x1b[4m"
	ansiUnderlineOff   = "\x1b[24m"
	maxTableCols       = 32
	maxTableRows       = 2048
	maxTableBytes      = 1 << 16
)

// renderDocument formats one complete Markdown segment. width <= 0 disables wrapping.
func renderDocument(input string, width int, styled bool) string {
	if input == "" {
		return ""
	}
	trailingNewline := strings.HasSuffix(input, "\n")
	lines := strings.Split(input, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	var out strings.Builder
	var paragraph []string
	paragraphRawTail := false
	deferredHeadingItalicClose := false
	hadContent, afterBlank, afterHeading := false, false, false
	inFence := false
	fenceBodySeen, fenceStyleOpen, emptyFenceAtEnd := false, false, false
	fenceLength := 0

	newlineCount := func() int {
		value := stripANSI(out.String())
		count := 0
		for index := len(value) - 1; index >= 0 && value[index] == '\n'; index-- {
			count++
		}
		return count
	}
	ensureNewlines := func(count int) {
		if !hadContent {
			return
		}
		for newlineCount() < count {
			out.WriteByte('\n')
		}
	}
	startBlock := func() {
		if hadContent {
			want := 1
			if afterBlank || afterHeading {
				want = 2
			}
			ensureNewlines(want)
		}
		hadContent = true
		afterBlank, afterHeading = false, false
	}
	flushParagraphWith := func(deferFinalItalicClose bool) bool {
		if len(paragraph) == 0 {
			return false
		}
		startBlock()
		text, deferredItalicClose := "", false
		if paragraphRawTail {
			text = joinParagraphWithRawTail(paragraph, styled)
		} else if deferFinalItalicClose {
			text, deferredItalicClose = joinParagraphWithDeferredItalicClose(paragraph, styled)
		} else {
			text = joinParagraph(paragraph, styled)
		}
		out.WriteString(wrapStyled(text, width, 0))
		paragraph = nil
		paragraphRawTail = false
		return deferredItalicClose
	}
	flushParagraph := func() {
		flushParagraphWith(false)
	}
	emitLiteralEOFLine := func(line string, deferredPrefix bool) {
		flushParagraph()
		if deferredPrefix {
			// Indented incomplete prefixes defer line-start dispatch, so they do not
			// pay a pending heading blank before falling back literally.
			ensureNewlines(1)
		} else {
			startBlock()
		}
		out.WriteString(wrapStyled(line, width, 0))
		hadContent = true
		afterBlank, afterHeading = false, false
	}

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		hasNewline := index < len(lines)-1 || trailingNewline
		lineForScan := line
		if hasNewline {
			lineForScan += "\n"
		}
		if inFence {
			if isFenceCloser(line, fenceLength) {
				if fenceStyleOpen {
					out.WriteString(terminaltheme.Close(terminaltheme.CodeBlock))
				}
				inFence = false
				fenceStyleOpen = false
				emptyFenceAtEnd = !fenceBodySeen
				fenceBodySeen = false
				continue
			}
			fenceBodySeen = true
			emptyFenceAtEnd = false
			out.WriteString(strings.ReplaceAll(line, "\t", "    "))
			if hasNewline {
				out.WriteByte('\n')
			}
			continue
		}

		info := ScanLine(lineForScan, !hasNewline)
		emptyFenceAtEnd = false
		if !hasNewline && info.Kind == LineText {
			streamingInfo := ScanLine(line, false)
			candidate := line[streamingInfo.IndentLength:]
			shortFence := streamingInfo.Kind == LineIncomplete
			bareFence := streamingInfo.Kind == LineFence && !streamingInfo.Complete && streamingInfo.IndentLength == 0
			if len(candidate) > 0 && strings.Trim(candidate, "`") == "" &&
				(bareFence || shortFence && len(paragraph) == 0) {
				// A fence candidate without content is still ambiguous at EOF upstream,
				// so it falls back literally instead of opening inline code.
				emitLiteralEOFLine(line, streamingInfo.IndentLength > 0)
				continue
			}
			if shortFence && len(paragraph) > 0 {
				// A short ambiguous prefix after prose is soft-joined by the upstream
				// newline handler, which copies its final tail without inline parsing.
				paragraphRawTail = true
			}
		}
		if !hasNewline && info.Kind == LineText {
			streamingInfo := ScanLine(line, false)
			if streamingInfo.Kind == LineFence && !streamingInfo.Complete && streamingInfo.NormalizeIndent {
				// The streaming scanner commits eligible fence indentation before it retains
				// an unterminated opener for EOF resolution. At EOF that candidate falls back
				// literally instead of reaching inline-code parsing.
				line = line[streamingInfo.IndentLength:]
				emitLiteralEOFLine(line, streamingInfo.IndentLength > 0)
				continue
			}
		}
		if !hasNewline && strings.TrimSpace(line) == "" && len(paragraph) > 0 {
			// A whitespace-only EOF line terminates the preceding prose line; it cannot
			// become a soft-joined space because no following content exists.
			flushParagraph()
			ensureNewlines(1)
			continue
		}
		if !hasNewline && index == len(lines)-1 {
			streamingInfo := ScanLine(line, false)
			if streamingInfo.Kind == LineBullet && !streamingInfo.Complete {
				// A terminal `* ` or `- ` stayed ambiguous with a thematic break
				// during Feed. The preceding newline was already a block boundary;
				// resolve only this marker literally at EOF.
				flushParagraph()
				startBlock()
				out.WriteString(line)
				continue
			}
		}
		if len(paragraph) > 0 && info.IndentLength > 3 &&
			(info.Kind == LineBullet || info.Kind == LineOrdered) {
			// After prose, CommonMark's four-space continuation is a soft join.
			// The upstream line scanner recognizes the marker, then the newline
			// handler consumes its indentation before inline dispatch, leaving the
			// marker literal instead of starting a nested list.
			paragraph = append(paragraph, line[info.IndentLength:])
			continue
		}
		switch info.Kind {
		case LineBlank:
			flushParagraph()
			if hadContent {
				ensureNewlines(2)
				afterBlank = true
				afterHeading = false
			}
		case LineFence:
			flushParagraph()
			startBlock()
			inFence, fenceLength = true, info.MarkerLength
			fenceBodySeen = false
			if styled {
				out.WriteString(terminaltheme.Open(terminaltheme.CodeBlock))
				fenceStyleOpen = true
			}
			emptyFenceAtEnd = true
		case LineHeading:
			flushParagraph()
			startBlock()
			heading := strings.TrimLeft(line[info.IndentLength+info.MarkerLength:], " \t")
			heading = strings.ReplaceAll(heading, "\t", "    ")
			heading = trimClosingHashes(heading)
			headingOpen := terminaltheme.Open(terminaltheme.Heading)
			headingClose := terminaltheme.Close(terminaltheme.Heading)
			if styled {
				out.WriteString(headingOpen)
			}
			headingText, headingItalicOpen := "", false
			if index == len(lines)-1 {
				headingText, headingItalicOpen = renderInlineWithDeferredItalicClose(heading, styled)
			} else {
				headingText = renderInline(heading, styled)
			}
			if styled {
				// SGR 22 closes the heading's bold too; foreground resets close its color.
				headingText = strings.ReplaceAll(headingText, ansiBoldOff, ansiBoldOff+headingOpen)
				if headingOpen != ansiBold {
					seenClose := make(map[string]struct{}, 2)
					for _, role := range []terminaltheme.Role{terminaltheme.CodeInline, terminaltheme.Link} {
						close := terminaltheme.Close(role)
						if close != "" {
							if _, seen := seenClose[close]; seen {
								continue
							}
							seenClose[close] = struct{}{}
							headingText = strings.ReplaceAll(headingText, close, close+headingOpen)
						}
					}
				}
			}
			out.WriteString(headingText)
			if styled {
				out.WriteString(headingClose)
				if headingItalicOpen {
					if trailingNewline {
						deferredHeadingItalicClose = true
					} else {
						out.WriteString(ansiItalicOff)
					}
				}
			}
			afterHeading = true
		case LineBullet:
			flushParagraph()
			startBlock()
			itemLines := []string{strings.TrimLeft(line[info.IndentLength+info.MarkerLength:], " \t")}
			end := index
			for next := index + 1; next < len(lines); next++ {
				if strings.TrimSpace(lines[next]) == "" {
					break
				}
				nextLine := lines[next]
				if next < len(lines)-1 || trailingNewline {
					nextLine += "\n"
				}
				nextIsFinal := next == len(lines)-1 && !trailingNewline
				if ScanLine(nextLine, nextIsFinal).Kind != LineText ||
					nextIsFinal && isIncompleteFenceCandidate(nextLine) {
					break
				}
				itemLines = append(itemLines, lines[next])
				end = next
			}
			text := joinParagraph(itemLines, styled)
			bullet := "• "
			if styled {
				bullet = ansiDim + bullet + ansiBoldOff
			}
			out.WriteString(wrapStyled(strings.Repeat(" ", info.IndentLength)+bullet+text, width, info.IndentLength+2))
			if end < len(lines)-1 || trailingNewline {
				out.WriteByte('\n')
			}
			index = end
		case LineOrdered:
			flushParagraph()
			startBlock()
			markerEnd := info.IndentLength + info.MarkerLength
			indent := strings.Repeat(" ", info.IndentLength)
			marker := line[info.IndentLength:markerEnd] + " "
			itemLines := []string{strings.TrimLeft(line[markerEnd:], " \t")}
			end := index
			for next := index + 1; next < len(lines); next++ {
				if strings.TrimSpace(lines[next]) == "" {
					break
				}
				nextLine := lines[next]
				if next < len(lines)-1 || trailingNewline {
					nextLine += "\n"
				}
				nextIsFinal := next == len(lines)-1 && !trailingNewline
				if ScanLine(nextLine, nextIsFinal).Kind != LineText ||
					nextIsFinal && isIncompleteFenceCandidate(nextLine) {
					break
				}
				itemLines = append(itemLines, lines[next])
				end = next
			}
			text := joinParagraph(itemLines, styled)
			if styled {
				marker = ansiDim + marker + ansiBoldOff
			}
			out.WriteString(wrapStyled(indent+marker+text, width, info.IndentLength+terminalwidth.Cells(line[info.IndentLength:markerEnd])+1))
			if end < len(lines)-1 || trailingNewline {
				out.WriteByte('\n')
			}
			index = end
		case LineThematic:
			flushParagraph()
			startBlock()
			if info.Marker == '=' {
				out.WriteString(wrapStyled(line, width, 0))
				if hasNewline {
					out.WriteByte('\n')
				}
			} else {
				dots := 3
				for width > 0 && dots > 1 && dots+(dots-1)*3 > width {
					dots--
				}
				var divider strings.Builder
				for dot := 0; dot < dots; dot++ {
					if dot > 0 {
						divider.WriteString("   ")
					}
					divider.WriteString("·")
				}
				dividerText := divider.String()
				if styled {
					dividerText = ansiDim + dividerText + ansiBoldOff
				}
				out.WriteString(dividerText)
				out.WriteByte('\n')
			}
		case LineBlockquote, LinePipe:
			flushParagraph()
			if info.NormalizeIndent {
				line = line[info.IndentLength:]
			}
			if info.Kind == LinePipe {
				if rows, alignments, end, ok := parseTable(lines, index, trailingNewline); ok {
					startBlock()
					literalTail := ""
					if !trailingNewline && end+1 == len(lines)-1 && !strings.Contains(lines[end+1], "|") {
						literalTail = lines[end+1]
					}
					if rows == nil {
						// Upstream keeps a collected table verbatim when its body shape or
						// row count exceeds the bounded formatter's assumptions.
						for row := index; row <= end; row++ {
							out.WriteString(wrapStyled(lines[row], width, 0))
							if row < len(lines)-1 || trailingNewline {
								out.WriteByte('\n')
							}
						}
					} else {
						out.WriteString(renderTable(rows, alignments, width, styled))
					}
					out.WriteString(wrapStyled(literalTail, width, 0))
					if literalTail != "" {
						end++
					}
					index = end
					continue
				}
				hasTableLookahead := index+1 < len(lines) && (index+2 < len(lines) || trailingNewline)
				if !hasTableLookahead {
					// Upstream defers a leading-pipe candidate until it sees a newline after
					// the possible delimiter row; unresolved bytes fall back literally.
					startBlock()
					var raw strings.Builder
					for row := index; row < len(lines); row++ {
						if row > index {
							raw.WriteByte('\n')
						}
						if row == index {
							raw.WriteString(line)
						} else {
							raw.WriteString(lines[row])
						}
					}
					if trailingNewline {
						raw.WriteByte('\n')
					}
					out.WriteString(wrapStyled(raw.String(), width, 0))
					index = len(lines) - 1
					continue
				}
			}
			startBlock()
			out.WriteString(wrapStyled(renderInline(line, styled), width, 0))
			if hasNewline {
				out.WriteByte('\n')
			}
		default:
			paragraph = append(paragraph, line)
		}
	}
	deferredItalicClose := flushParagraphWith(trailingNewline) || deferredHeadingItalicClose
	if inFence && fenceStyleOpen {
		out.WriteString(terminaltheme.Close(terminaltheme.CodeBlock))
	}
	if trailingNewline && hadContent && !emptyFenceAtEnd {
		ensureNewlines(1)
	}
	if deferredItalicClose && styled {
		out.WriteString(ansiItalicOff)
	}
	return out.String()
}

// HasTrailingNewline reports whether output ends after a visible line break, ignoring ANSI CSI
// sequences which may follow that line break to restore terminal style.
func HasTrailingNewline(input string) bool {
	return strings.HasSuffix(stripANSI(input), "\n")
}

func joinParagraph(lines []string, styled bool) string {
	text, _ := joinParagraphWithOptions(lines, styled, false)
	return text
}

func joinParagraphWithDeferredItalicClose(lines []string, styled bool) (string, bool) {
	return joinParagraphWithOptions(lines, styled, true)
}

func joinParagraphWithOptions(lines []string, styled, deferItalicClose bool) (string, bool) {
	var out strings.Builder
	for index, line := range lines {
		if index > 0 {
			previous := lines[index-1]
			previousWithoutCR := strings.TrimSuffix(previous, "\r")
			trailingSpaces := len(previousWithoutCR) - len(strings.TrimRight(previousWithoutCR, " "))
			codeAtNewline := inlineCodeOpenAtEnd(previousWithoutCR)
			hardBreak := trailingSpaces >= 2 && !codeAtNewline
			out.WriteByte(inlineLineBoundary)
			if hardBreak {
				out.WriteString(strings.Repeat(" ", trailingSpaces))
				if strings.HasSuffix(previous, "\r") {
					out.WriteByte('\r')
				}
				out.WriteByte('\n')
			} else {
				trailingWhitespace := previousWithoutCR[len(strings.TrimRight(previousWithoutCR, " \t")):]
				if trailingWhitespace == "" {
					out.WriteByte(' ')
				} else if !codeAtNewline {
					out.WriteString(trailingWhitespace)
				}
				line = strings.TrimLeft(line, " \t")
			}
		}
		if index < len(lines)-1 {
			previousWithoutCR := strings.TrimSuffix(line, "\r")
			trailingSpaces := len(previousWithoutCR) - len(strings.TrimRight(previousWithoutCR, " "))
			if inlineCodeOpenAtEnd(previousWithoutCR) {
				// The upstream inline-code handler emits the trailing bytes, closes code
				// at the newline, and clears hard-break whitespace state.
			} else if trailingSpaces >= 2 {
				line = strings.TrimRight(line, " \r")
			} else {
				line = strings.TrimRight(line, " \t\r")
			}
		}
		out.WriteString(line)
	}
	if deferItalicClose {
		return renderInlineWithDeferredItalicClose(out.String(), styled)
	}
	return renderInline(out.String(), styled), false
}

func joinParagraphWithRawTail(lines []string, styled bool) string {
	if len(lines) < 2 {
		return joinParagraph(lines, styled)
	}

	previous := lines[len(lines)-2]
	previousWithoutCR := strings.TrimSuffix(previous, "\r")
	trailingSpaces := len(previousWithoutCR) - len(strings.TrimRight(previousWithoutCR, " "))
	var out strings.Builder
	out.WriteString(joinParagraph(lines[:len(lines)-1], styled))
	if trailingSpaces >= 2 && !inlineCodeOpenAtEnd(previousWithoutCR) {
		out.WriteByte('\n')
	} else if len(previous) > 0 && previous[len(previous)-1] != ' ' && previous[len(previous)-1] != '\t' {
		out.WriteByte(' ')
	}
	out.WriteString(strings.TrimLeft(lines[len(lines)-1], " \t"))
	return out.String()
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

func renderInlineWithState(input string, styled, suppressBold, deferItalicClose bool) (string, bool) {
	var out strings.Builder
	state := inlineState{
		styled: styled, suppressBold: suppressBold, deferFinalItalicClose: deferItalicClose,
	}
	state.render(&out, input)
	return out.String(), state.leftItalicOpen
}

type inlineState struct {
	styled                bool
	suppressBold          bool
	pendingDash           bool
	previousSpace         bool
	hasPrevious           bool
	unmatchedItalic       bool
	deferFinalItalicClose bool
	leftItalicOpen        bool
}

func (state *inlineState) writeText(out *strings.Builder, text string) {
	if text == "" {
		return
	}
	if state.pendingDash {
		state.pendingDash = false
		if !isMarkdownSpace(text[0]) {
			out.WriteByte(' ')
		}
	}
	out.WriteString(text)
	state.previousSpace = isMarkdownSpace(text[len(text)-1])
	state.hasPrevious = true
}

func (state *inlineState) render(out *strings.Builder, input string) {
	for index := 0; index < len(input); {
		if input[index] == inlineLineBoundary {
			index++
			continue
		}
		if strings.HasPrefix(input[index:], "—") {
			if state.hasPrevious && !state.previousSpace {
				out.WriteByte(' ')
			}
			out.WriteString("—")
			state.previousSpace, state.hasPrevious, state.pendingDash = false, true, true
			index += len("—")
			continue
		}
		if end, ok := bareURL(input, index); ok {
			if state.pendingDash {
				state.pendingDash = false
				out.WriteByte(' ')
			}
			if state.styled {
				out.WriteString(terminaltheme.Open(terminaltheme.Link))
			}
			state.writeText(out, input[index:end])
			if state.styled {
				out.WriteString(terminaltheme.Close(terminaltheme.Link))
			}
			index = end
			continue
		}
		if input[index] == '`' {
			contentStart := index + 1
			boundary := strings.IndexByte(input[contentStart:], inlineLineBoundary)
			searchEnd := len(input)
			if boundary >= 0 {
				searchEnd = contentStart + boundary
			}
			if closeAt := strings.IndexByte(input[contentStart:searchEnd], '`'); closeAt >= 0 {
				contentEnd := contentStart + closeAt
				if state.styled {
					out.WriteString(terminaltheme.Open(terminaltheme.CodeInline))
				}
				state.writeText(out, input[contentStart:contentEnd])
				if state.styled {
					out.WriteString(terminaltheme.Close(terminaltheme.CodeInline))
				}
				index = contentEnd + 1
				continue
			}
			if boundary >= 0 {
				if state.styled {
					out.WriteString(terminaltheme.Open(terminaltheme.CodeInline))
				}
				state.writeText(out, input[contentStart:searchEnd])
				if state.styled {
					out.WriteString(terminaltheme.Close(terminaltheme.CodeInline))
				}
				index = searchEnd + 1
				continue
			}
			// An unterminated inline-code opener consumes the rest of this inline
			// segment, matching the streaming parser's EOF flush.
			if state.styled {
				out.WriteString(terminaltheme.Open(terminaltheme.CodeInline))
			}
			state.writeText(out, input[contentStart:])
			if state.styled {
				out.WriteString(terminaltheme.Close(terminaltheme.CodeInline))
			}
			return
		}
		if state.unmatchedItalic && (input[index] == '_' ||
			input[index] == '*' && (index+1 >= len(input) || input[index+1] != '*')) {
			if state.styled {
				out.WriteString(ansiItalicOff)
			}
			state.unmatchedItalic = false
			index++
			continue
		}
		if input[index] == '_' && index+1 < len(input) && input[index+1] == '_' {
			left := byte(0)
			if index > 0 {
				left = input[index-1]
			}
			if canOpenEmphasis(left, input[index+1]) {
				// The upstream parser processes each underscore independently: the first
				// opens italic and the adjacent second underscore closes it immediately.
				if state.styled {
					out.WriteString(ansiItalic)
					out.WriteString(ansiItalicOff)
				}
				index += 2
				continue
			}
		}
		marker, open, close := "", "", ""
		if index+1 < len(input) && input[index:index+2] == "**" {
			marker, open, close = input[index:index+2], ansiBold, ansiBoldOff
		} else if input[index] == '*' || input[index] == '_' {
			marker, open, close = input[index:index+1], ansiItalic, ansiItalicOff
		}
		if marker != "" {
			right := index + len(marker)
			left := byte(0)
			if index > 0 {
				left = input[index-1]
			}
			if right < len(input) && canOpenEmphasis(left, input[right]) {
				closeAt := strings.Index(input[right:], marker)
				if len(marker) == 1 {
					closeAt = findItalicCloser(input[right:])
				}
				if closeAt == 0 && len(marker) == 1 && input[right] != marker[0] &&
					(input[right] == '*' || input[right] == '_') {
					if state.styled {
						out.WriteString(ansiItalic)
						out.WriteString(ansiItalicOff)
					}
					index = right + 1
					continue
				}
				if closeAt > 0 {
					contentEnd := right + closeAt
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						out.WriteString(open)
					}
					state.render(out, input[right:contentEnd])
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						out.WriteString(close)
					}
					index = contentEnd + len(marker)
					continue
				}
				if closeAt < 0 {
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						out.WriteString(open)
					}
					unmatchedItalic := len(marker) == 1
					if unmatchedItalic {
						state.unmatchedItalic = true
					}
					state.render(out, input[right:])
					closedInside := unmatchedItalic && !state.unmatchedItalic
					leftOpen := false
					if unmatchedItalic && state.unmatchedItalic {
						state.unmatchedItalic = false
						if state.deferFinalItalicClose {
							state.leftItalicOpen = true
							leftOpen = true
						}
					}
					if state.styled && !closedInside && !leftOpen && !(state.suppressBold && len(marker) == 2) {
						out.WriteString(close)
					}
					return
				}
			}
			if len(marker) == 2 {
				state.writeText(out, marker)
				index += 2
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(input[index:])
		state.writeText(out, input[index:index+size])
		index += size
	}
}

func isMarkdownSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func bareURL(input string, index int) (int, bool) {
	if index > 0 && isASCIIAlnum(input[index-1]) {
		return index, false
	}
	remaining := strings.ToLower(input[index:])
	schemeLength := 0
	if strings.HasPrefix(remaining, "https://") {
		schemeLength = len("https://")
	} else if strings.HasPrefix(remaining, "http://") {
		schemeLength = len("http://")
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

func trimClosingHashes(text string) string {
	trimmed := strings.TrimRight(text, " \t")
	end := len(trimmed)
	hashEnd := end
	for end > 0 && text[end-1] == '#' {
		end--
	}
	if end < hashEnd && (end == 0 || text[end-1] == ' ' || text[end-1] == '\t') {
		return strings.TrimRight(text[:end], " \t")
	}
	return text
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
