package markdown

import (
	"strings"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

// parser renders Markdown as it arrives, holding only the bytes a later byte could still change.
//
// It renders the same blocks, separators and bytes as the whole-document path, but
// driven one line at a time so a caller can show prose while a response is still arriving. The
// batch renderer stays the reference for every rule, and a differential test pins the two together.
//
// Two holds make that possible. Prose streams as it arrives, because the inline walker settles
// everything except its ambiguous tail. A line's *boundary* — whether the paragraph continues, and
// therefore whether the line is trimmed and what separator follows — waits for the next line's
// first bytes, which is the deferral the newline handler performs.
type parser struct {
	width  int
	styled bool
	write  func(string) error

	pending    string // received bytes not yet consumed
	sawNewline bool   // the last consumed line was newline-terminated
	err        error

	// Separator state: the same rules the batch renderer applied when it accumulated a document.
	hadContent       bool
	afterBlank       bool
	afterHeading     bool
	trailingNewlines int

	mode      parserMode
	wrap      terminalWrapper
	inline    inlineState
	unsettled string // inline bytes the walker has not settled yet

	// Prose being streamed: a paragraph or a list item.
	line         string // current line, as the batch's paragraph list holds it
	fed          int    // bytes of line already handed to the inline walker
	lineComplete bool   // the line's terminating newline has arrived
	itemEnd      bool   // a list item owes its terminating newline when it closes

	// Fenced code.
	fenceLength       int
	fenceStyleOpen    bool
	fenceStyleWritten bool
	fenceBodySeen     bool
	emptyFenceAtEnd   bool

	// Table collection.
	tableHeld     string // the leading-pipe line waiting for its delimiter row
	tableLines    []string
	tableBytes    int
	tableVerbatim bool

	// A heading's rendered tail waits until the next line says whether it was the last line.
	headingSet     bool
	headingText    string
	headingItalic  bool
	headingNewline bool

	deferredItalicClose bool
}

type parserMode uint8

const (
	modeBlock parserMode = iota
	modeProse
	modeFence
	modeTableStart
	modeTable
)

func newParser(width int, styled bool, write func(string) error) *parser {
	return &parser{
		width:  width,
		styled: styled,
		write:  write,
		inline: inlineState{styled: styled},
	}
}

// feed consumes as much of input as can be settled. With final set, what remains is resolved
// against end of input instead of being held for more.
//
// A final feed of new bytes settles them as ordinary input first and only then resolves the tail,
// so feeding a whole document at once renders exactly what feeding it and closing would.
func (p *parser) feed(input string, final bool) {
	if p.err != nil {
		return
	}
	p.pending += input
	p.consume(false)
	if final {
		p.consume(true)
		if p.err == nil {
			p.finish()
		}
	}
}

// consume runs the step for the current mode until nothing more can be settled.
func (p *parser) consume(final bool) {
	for p.err == nil {
		progress := false
		switch p.mode {
		case modeProse:
			progress = p.stepProse(final)
		case modeFence:
			progress = p.stepFence(final)
		case modeTableStart:
			progress = p.stepTableStart(final)
		case modeTable:
			progress = p.stepTable(final)
		default:
			progress = p.stepBlock(final)
		}
		if !progress {
			break
		}
	}
}

// finish resolves the document end: open constructs close, a held heading resolves against end of
// input, and the trailing newline and deferred italic close are paid.
func (p *parser) finish() {
	// The mode steps consume everything with final set, so only a fence can still be open here.
	if p.headingHeld() {
		p.emitHeading(true)
	}
	if p.mode == modeFence && p.fenceStyleOpen {
		// An unterminated fence still opens and closes its style, even with no body.
		p.openFenceStyle()
		p.raw(terminaltheme.Close(terminaltheme.CodeBlock))
	}
	if p.sawNewline && p.hadContent && !p.emptyFenceAtEnd {
		p.ensureNewlines(1)
	}
	if p.deferredItalicClose && p.styled {
		p.raw(ansiItalicOff)
	}
}

// discard resolves the styles a stream left open. Held payloads are dropped, which is what Reset
// promises, but the terminal must not be left styled by a sequence that was already written.
func (p *parser) discard() {
	if p.deferredItalicClose && p.styled {
		p.raw(ansiItalicOff)
	}
	if p.fenceStyleOpen && p.fenceStyleWritten {
		p.raw(terminaltheme.Close(terminaltheme.CodeBlock))
	}
	p.deferredItalicClose = false
	p.fenceStyleOpen = false
	p.fenceStyleWritten = false
	p.headingSet = false
	p.headingText = ""
}

// splitFirstLine separates the first physical line from the bytes after it. complete reports
// whether a newline terminated it.
func splitFirstLine(pending string) (line, rest string, complete bool) {
	if index := strings.IndexByte(pending, '\n'); index >= 0 {
		return pending[:index], pending[index+1:], true
	}
	return pending, "", false
}

// trailingRunStart is the first byte of a line's trailing whitespace run. A boundary decision can
// trim that run, so prose is not emitted past this point until the boundary is known.
func trailingRunStart(line string) int {
	index := len(line)
	for index > 0 && (line[index-1] == ' ' || line[index-1] == '\t' || line[index-1] == '\r') {
		index--
	}
	return index
}

// proseHoldStart is where a partial line stops being settled prose. A trailing whitespace run may
// still be trimmed and a trailing backtick run may still turn out to be a fence candidate at end of
// input, so both wait; a line that is still only whitespace and backticks waits entirely, because
// either outcome replaces it rather than continuing it.
func proseHoldStart(line string) int {
	if strings.Trim(line, " \t\r`") == "" {
		return 0
	}
	hold := trailingRunStart(line)
	backticks := len(line)
	for backticks > 0 && line[backticks-1] == '`' {
		backticks--
	}
	if backticks < hold {
		hold = backticks
	}
	return hold
}

// stepBlock dispatches one line at a block boundary.
func (p *parser) stepBlock(final bool) bool {
	if p.headingHeld() {
		// The next line's arrival is what tells a held heading it was not the last line.
		if p.pending == "" && !final {
			return false
		}
		p.emitHeading(p.pending == "" && final)
		if p.pending == "" {
			return false
		}
	}
	if p.pending == "" {
		return false
	}
	line, rest, complete := splitFirstLine(p.pending)
	if !complete && !final {
		// A prefix that is already settled prose starts the paragraph now, so prose streams
		// before its line ends. Anything else may still turn into a block marker, so it waits.
		if ScanLine(line, false).Kind != LineText {
			return false
		}
		p.pending = rest
		p.startProse(line, 0, false)
		return true
	}
	scan := line
	if complete {
		scan += "\n"
	}
	info := ScanLine(scan, !complete)
	// The document's trailing newline is the last consumed line's terminator, so it is recorded
	// before a final line resolves: a line without one owes no trailing newline.
	p.sawNewline = complete
	if !complete && p.stepFinalLine(line, info) {
		return true
	}

	switch info.Kind {
	case LineBlank:
		p.pending = rest
		p.blank()
	case LineFence:
		p.pending = rest
		p.openFence(info)
	case LineHeading:
		p.pending = rest
		if headingMarkerPresent(line, info) {
			p.startHeading(line, info, complete)
		} else {
			// The scanner reports a heading for hashes followed by a newline, but the dispatcher
			// requires a space, so such a line stays prose.
			p.startProse(line, 0, complete)
		}
	case LineThematic:
		p.pending = rest
		p.startThematic(line, info, complete)
	case LineBullet, LineOrdered:
		p.pending = rest
		p.startListItem(line, info)
	case LineBlockquote:
		p.pending = rest
		p.startBlockquote(line, info, complete)
	case LinePipe:
		p.tableHeld = line
		p.pending = rest
		p.mode = modeTableStart
	case LineIncomplete:
		if !complete {
			return false
		}
		p.pending = rest
		p.startProse(line, 0, complete)
	default:
		p.pending = rest
		p.startProse(line, 0, complete)
	}
	return true
}

// headingMarkerPresent reports whether the byte after a heading's hashes is a space, which the
// dispatcher requires even though the scanner also accepts a newline.
func headingMarkerPresent(line string, info LineInfo) bool {
	index := info.IndentLength + info.MarkerLength
	return index < len(line) && line[index] == ' '
}

// stepFinalLine resolves the end-of-input cases where a line's own bytes decide its kind: a fence
// candidate with no content, an indented incomplete prefix, a whitespace-only continuation, and a
// terminal bullet that stayed ambiguous with a thematic break. The prose cases live in
// stepFinalProseLine, which runs while a paragraph is still open.
func (p *parser) stepFinalLine(line string, info LineInfo) bool {
	streaming := ScanLine(line, false)
	candidate := line[streaming.IndentLength:]
	allBackticks := len(candidate) > 0 && strings.Trim(candidate, "`") == ""
	bareFence := streaming.Kind == LineFence && !streaming.Complete && streaming.IndentLength == 0
	shortFence := streaming.Kind == LineIncomplete

	if allBackticks && (bareFence || shortFence) {
		// A fence candidate without content is still ambiguous at end of input, so it falls back
		// literally instead of opening inline code.
		p.closeProseForBlock()
		p.emitLiteralEOFLine(line, streaming.IndentLength > 0)
		p.pending = ""
		return true
	}
	if info.Kind == LineText && streaming.Kind == LineFence && !streaming.Complete &&
		streaming.NormalizeIndent {
		// The streaming scanner commits eligible fence indentation before it retains an
		// unterminated opener for EOF resolution.
		p.closeProseForBlock()
		p.emitLiteralEOFLine(candidate, streaming.IndentLength > 0)
		p.pending = ""
		return true
	}
	if streaming.Kind == LineBullet && !streaming.Complete {
		// A terminal `* ` or `- ` stayed ambiguous with a thematic break. The preceding newline
		// was already a block boundary; resolve only this marker literally at EOF.
		p.closeProseForBlock()
		p.startBlock()
		p.raw(line)
		p.pending = ""
		return true
	}
	return false
}

// emitLiteralEOFLine writes a line whose prefix stayed ambiguous, exactly as the batch's literal
// fallback does.
func (p *parser) emitLiteralEOFLine(line string, deferredPrefix bool) {
	if deferredPrefix {
		// Indented incomplete prefixes defer line-start dispatch, so they do not pay a pending
		// heading blank before falling back literally.
		p.ensureNewlines(1)
	} else {
		p.startBlock()
	}
	p.raw(wrapStyled(line, p.width, 0))
	p.hadContent = true
	p.afterBlank, p.afterHeading = false, false
}

// stepProse streams the current prose line and decides its boundary once the next line's kind is
// known. It returns false when it needs more input.
//
// In prose mode p.pending holds the bytes after p.line: the current line's continuation while that
// line is still open, then the next line's bytes once it closed.
func (p *parser) stepProse(final bool) bool {
	if !p.lineComplete {
		next, rest, complete := splitFirstLine(p.pending)
		if complete || !final {
			p.sawNewline = complete
		}
		if !complete && !final {
			p.line += p.pending
			p.pending = ""
			p.feedProseLine()
			return false
		}
		p.line += next
		p.pending = rest
		p.lineComplete = complete
	}
	p.feedProseLine()

	if p.pending == "" {
		if !final {
			return false
		}
		// No line follows: this is the paragraph's last line.
		p.closeProse(true, false)
		return true
	}
	next, rest, nextComplete := splitFirstLine(p.pending)
	nextScan := next
	if nextComplete {
		nextScan += "\n"
	}
	// A list item's continuation follows the batch's item loop instead, which only stops at a
	// blank line, a block, or a line that could still open a fence.
	// A line whose classification is not settled is not joined to the paragraph yet: it is
	// deferred, and at end of input emitted literally instead of inline-parsed. Waiting for
	// one more byte decides whether it is the document's last line. A line that is still only
	// whitespace waits for the same reason.
	streaming := ScanLine(next, false)
	ambiguous := (streaming.Kind == LineIncomplete ||
		streaming.Kind == LineFence && !streaming.Complete) &&
		ScanLine(nextScan, true).Kind == LineText
	if ambiguous && rest == "" && !final {
		return false
	}
	if !nextComplete && strings.TrimSpace(next) == "" && !final {
		return false
	}
	if final && ScanLine(next, true).Kind == LineText && p.stepFinalProseLine(next, nextComplete) {
		return true
	}
	// Only a line at end of input is scanned as final: a partial line mid-stream is incomplete,
	// not last.
	nextInfo := ScanLine(nextScan, !nextComplete && final)
	if !nextComplete && !final && nextInfo.Kind == LineIncomplete {
		// The boundary depends on a line that is not classified yet.
		return false
	}
	p.sawNewline = true

	// The batch's block switch sends every kind it does not name to the paragraph list, so an
	// unsettled classification continues prose rather than starting a block.
	continues := nextInfo.Kind == LineText || nextInfo.Kind == LineIncomplete
	blankNext := strings.TrimSpace(next) == ""
	if p.itemEnd && blankNext && !nextComplete && !final {
		// The batch's item loop judges a whole line, so a line that is still only whitespace may
		// yet gain the content that decides whether the item continues.
		return false
	}
	if p.itemEnd && (blankNext || !nextComplete && isIncompleteFenceCandidate(next)) {
		// The batch's item loop stops at a blank line or a line that could still open a fence.
		continues = false
	}
	stripped := next
	if nextInfo.IndentLength > 3 && (nextInfo.Kind == LineBullet || nextInfo.Kind == LineOrdered) {
		// After prose, CommonMark's four-space continuation is a soft join: the marker stays
		// literal instead of starting a nested list.
		continues, stripped = true, next[nextInfo.IndentLength:]
	}
	if !continues {
		p.pending = nextScan + rest
		p.closeProse(false, true)
		return true
	}

	// The paragraph continues: write the line the way the batch's join does, then hold the next.
	body, separator, trimmedNext := paragraphBoundary(p.line, stripped)
	p.pending = rest
	if p.fed < len(body) {
		p.feedInline(body[p.fed:], false)
	}
	p.fed = len(body)
	p.feedInline(separator, false)
	p.line, p.fed, p.lineComplete = trimmedNext, 0, nextComplete
	return true
}

// stepFinalProseLine resolves the end-of-input cases where the line following prose decides it: a
// whitespace-only line, a backtick-only fence candidate, and a short ambiguous prefix the newline
// handler copies without inline parsing.
func (p *parser) stepFinalProseLine(next string, complete bool) bool {
	streaming := ScanLine(next, false)
	candidate := next[streaming.IndentLength:]
	allBackticks := len(candidate) > 0 && strings.Trim(candidate, "`") == ""
	bareFence := streaming.Kind == LineFence && !streaming.Complete && streaming.IndentLength == 0
	if !complete {
		// The document's last line has no terminator, so it owes no trailing newline.
		p.sawNewline = false
	}
	if strings.TrimSpace(next) == "" {
		// A whitespace-only EOF line terminates the preceding prose line; it cannot become a
		// soft-joined space because no following content exists.
		p.pending = ""
		p.closeProse(false, false)
		p.ensureNewlines(1)
		return true
	}
	if streaming.Kind == LineFence && !streaming.Complete && streaming.NormalizeIndent {
		// The streaming scanner commits eligible fence indentation before it retains an
		// unterminated opener for EOF resolution.
		p.pending = ""
		p.closeProseForBlock()
		p.emitLiteralEOFLine(candidate, streaming.IndentLength > 0)
		return true
	}
	if allBackticks && bareFence {
		// A fence candidate without content is still ambiguous at end of input, so it falls back
		// literally instead of opening inline code.
		p.pending = ""
		p.closeProseForBlock()
		p.emitLiteralEOFLine(next, streaming.IndentLength > 0)
		return true
	}
	if streaming.Kind == LineBullet && !streaming.Complete {
		// A terminal `* ` or `- ` stayed ambiguous with a thematic break during Feed. The
		// preceding newline was already a block boundary; resolve only this marker literally.
		p.pending = ""
		p.closeProseForBlock()
		p.startBlock()
		p.raw(next)
		return true
	}
	if streaming.Kind == LineIncomplete {
		// A short ambiguous prefix after prose is soft-joined by the newline handler, which
		// copies its final tail without inline parsing.
		p.pending = ""
		p.closeProseRawTail(next)
		return true
	}
	return false
}

// feedProseLine hands the current line's settled bytes to the inline walker. A trailing whitespace
// or backtick run waits: the boundary may trim the first, and end of input may replace the second.
func (p *parser) feedProseLine() {
	if hold := proseHoldStart(p.line); hold > p.fed {
		p.feedInline(p.line[p.fed:hold], false)
		p.fed = hold
	}
}

// paragraphBoundary returns the bytes the batch renderer writes for a paragraph line that a
// following line continues: the line itself as the join trims it, the boundary sentinel, and the
// separator that replaces the newline.
func paragraphBoundary(line, next string) (body, separator, nextTrimmed string) {
	// The parser never rewrites a line: it emits the bytes as they arrive and decides only what
	// replaces the newline. A carriage return is therefore a text byte that also ends the trailing
	// space run, and a hard break is a newline on its own rather than a re-emission of the spaces.
	// A carriage return does not end the run: the parser keeps counting the spaces before it.
	withoutCR := strings.TrimSuffix(line, "\r")
	trailingSpaces := len(withoutCR) - len(strings.TrimRight(withoutCR, " "))
	var out strings.Builder
	out.WriteByte(inlineLineBoundary)
	if trailingSpaces >= 2 && !inlineCodeOpenAtEnd(line) {
		out.WriteByte('\n')
		return line, out.String(), next
	}
	// A soft join adds a space unless the line already ended in one, and skips the whitespace the
	// next line starts with.
	if line == "" || line[len(line)-1] != ' ' && line[len(line)-1] != '\t' {
		out.WriteByte(' ')
	}
	return line, out.String(), strings.TrimLeft(next, " \t")
}

// startProse begins a paragraph, or a list item whose prefix is already written. The line is the
// one just dispatched, so p.pending already holds the bytes after it.
func (p *parser) startProse(line string, indent int, complete bool) {
	p.startBlock()
	p.line, p.fed, p.lineComplete = line, 0, complete
	p.mode = modeProse
	p.inline = inlineState{styled: p.styled}
	p.unsettled = ""
	p.wrap = terminalWrapper{width: p.width, requestedIndent: indent, lastBreakByte: -1}
}

// closeProse finishes the current paragraph or list item. atEOF marks the document's last block,
// whose unterminated italic close waits for the document end; followed marks that a line or block
// follows, which is what makes a list item write its own terminating newline.
func (p *parser) closeProse(atEOF, followed bool) {
	if p.mode != modeProse {
		return
	}
	if p.fed < len(p.line) {
		p.feedInline(p.line[p.fed:], false)
	}
	p.fed = len(p.line)
	if atEOF && !p.lineComplete {
		p.sawNewline = false
	}
	p.inline.deferFinalItalicClose = atEOF && p.sawNewline
	p.feedInline("", true)
	if p.inline.leftItalicOpen {
		p.inline.leftItalicOpen = false
		p.deferredItalicClose = true
	}
	p.wrap.finish()
	p.flushWrap()
	if p.itemEnd {
		// A list item, like a heading, writes its own terminating newline.
		if followed || p.sawNewline {
			p.raw("\n")
		}
		p.itemEnd = false
	} else if followed {
		// A paragraph's terminating separator is owed to the block that follows, and once the
		// paragraph has closed no later byte can change it, so it is paid now rather than when
		// that block's own line completes.
		p.startBlock()
	}
	p.line, p.fed, p.lineComplete = "", 0, false
	p.mode = modeBlock
}

// closeProseForBlock ends prose because a block follows, so its last line is not the document's
// last.
func (p *parser) closeProseForBlock() {
	if p.mode == modeProse {
		p.closeProse(false, true)
	}
}

// closeProseRawTail joins a short ambiguous final line the way the batch's raw-tail join does. That
// join copies the line without inline parsing, keeps the preceding line untrimmed, and writes no
// boundary sentinel, so it cannot reuse paragraphBoundary.
func (p *parser) closeProseRawTail(last string) {
	if p.mode != modeProse {
		return
	}
	if p.fed < len(p.line) {
		p.feedInline(p.line[p.fed:], false)
	}
	p.fed = len(p.line)
	// The raw-tail join writes its separator, then the tail as literal bytes: the newline handler's
	// handler emits them while the run they sit inside is still open, and md_flush closes that run
	// afterwards.
	withoutCR := strings.TrimSuffix(p.line, "\r")
	trailingSpaces := len(withoutCR) - len(strings.TrimRight(withoutCR, " "))
	switch {
	case trailingSpaces >= 2 && !inlineCodeOpenAtEnd(p.line):
		p.feedInline("\n", false)
	case len(p.line) > 0 && p.line[len(p.line)-1] != ' ' && p.line[len(p.line)-1] != '\t':
		p.feedInline(" ", false)
	}
	p.inline.deferFinalItalicClose = false
	p.feedInline(string(inlineLiteralTail)+strings.TrimLeft(last, " \t"), false)
	p.feedInline("", true)
	p.line, p.fed, p.lineComplete = "", 0, false
	p.mode = modeBlock
}

// writeProse writes settled prose bytes outside the inline walker, through the wrapper only when
// wrapping is on, since a non-positive width keeps carriage returns and expands tabs differently.
func (p *parser) writeProse(text string) {
	text = p.expandTabs(text)
	if p.width <= 0 {
		p.raw(text)
		return
	}
	p.wrap.write(text)
	p.wrap.finish()
	p.flushWrap()
}

// expandTabs replaces tabs the way wrapStyled does before wrapping.
func (p *parser) expandTabs(text string) string {
	tabReplacement := "    "
	if p.width > 0 {
		tabReplacement = " "
	}
	return strings.ReplaceAll(text, "\t", tabReplacement)
}

// stepFence consumes fenced code, which the batch copies through without inline parsing.
func (p *parser) stepFence(final bool) bool {
	line, rest, complete := splitFirstLine(p.pending)
	if !complete && !final {
		return false
	}
	p.pending = rest
	p.sawNewline = complete
	if isFenceCloser(line, p.fenceLength) {
		// End of input terminates a valid closer followed only by optional whitespace, so the
		// closer is consumed rather than copied into the body.
		p.openFenceStyle()
		if p.fenceStyleOpen {
			p.raw(terminaltheme.Close(terminaltheme.CodeBlock))
		}
		p.fenceStyleOpen = false
		p.emptyFenceAtEnd = !p.fenceBodySeen
		p.fenceBodySeen = false
		p.mode = modeBlock
		return true
	}
	p.fenceBodySeen = true
	p.emptyFenceAtEnd = false
	if line != "" {
		p.openFenceStyle()
		p.raw(strings.ReplaceAll(line, "\t", "    "))
	}
	if complete {
		p.raw("\n")
	}
	return complete
}

// stepTableStart holds a leading-pipe line until the line that could be its delimiter row is
// complete, which is the only lookahead the table decision needs.
func (p *parser) stepTableStart(final bool) bool {
	line, rest, complete := splitFirstLine(p.pending)
	if !complete && !final {
		return false
	}
	if p.pending != "" {
		p.sawNewline = complete
	}
	if _, _, _, ok := parseTable([]string{p.tableHeld, line}, 0, complete); !ok {
		if !complete {
			// No delimiter row arrived: the unresolved bytes fall back literally.
			p.emitLiteralTail()
			return true
		}
		// Not a table: this line renders inline and the next line dispatches normally.
		p.pending = line + "\n" + rest
		p.mode = modeBlock
		p.startPipeLiteral()
		return true
	}
	p.pending = rest
	p.tableLines = []string{p.tableHeld, line}
	p.tableBytes = len(p.tableHeld) + len(line) + 2
	p.tableVerbatim = false
	p.mode = modeTable
	return true
}

// stepTable collects table rows until a line disqualifies itself, then renders the table.
func (p *parser) stepTable(final bool) bool {
	if p.pending == "" {
		// Nothing has arrived for a row yet, so the table's own last line still decides the
		// trailing newline.
		if !final {
			return false
		}
		p.finalizeTable(p.sawNewline)
		return true
	}
	line, rest, complete := splitFirstLine(p.pending)
	if !complete && !final {
		p.sawNewline = false
		// An oversized partial row is already lossless as literal text, and the parser bails the
		// whole attempt through its literal path rather than retaining it.
		if p.tableBytes+len(line) > maxTableBytes {
			p.tableLines = append(p.tableLines, line)
			p.tableVerbatim = true
			p.pending = ""
			p.finalizeTable(false)
			return true
		}
		return false
	}
	last := !complete
	qualifies := false
	var row []string
	p.sawNewline = complete
	row = splitTableRow(line)
	qualifies = strings.Contains(line, "|") && len(row) > 0
	if qualifies && p.tableBytes+len(line)+1 > maxTableBytes {
		if last {
			// An oversized unterminated tail is still part of the active table attempt; the parser
			// bails the whole attempt through its lossless literal path.
			p.tableLines = append(p.tableLines, line)
			p.tableVerbatim = true
			p.pending = ""
			p.finalizeTable(false)
			return true
		}
		qualifies = false
	}
	if qualifies {
		p.pending = rest
		p.tableBytes += len(line) + 1
		p.tableLines = append(p.tableLines, line)
		if len(row) > len(splitTableRow(p.tableLines[0])) || len(p.tableLines) > maxTableRows {
			p.tableVerbatim = true
		}
		return true
	}
	// This line is not a row: the table ends before it.
	if last && !strings.Contains(line, "|") {
		// A rejected final row is table fallback output, not fresh Markdown input.
		p.pending = ""
		p.finalizeTable(false)
		p.raw(wrapStyled(line, p.width, 0))
		return true
	}
	// The rejected line is dispatched normally, so it stays pending.
	p.pending = line + "\n" + rest
	p.finalizeTable(true)
	if last {
		p.pending = line
	}
	return true
}

// finalizeTable renders the collected table and returns to block dispatch.
func (p *parser) finalizeTable(finalHasNewline bool) {
	p.mode = modeBlock
	p.startBlock()
	rows, alignments, _, _ := parseTable(p.tableLines, 0, finalHasNewline)
	if p.tableVerbatim || rows == nil {
		// A collected table is kept verbatim when its body shape or row count exceeds the
		// bounded formatter's assumptions.
		for index := 0; index < len(p.tableLines); index++ {
			p.raw(wrapStyled(p.tableLines[index], p.width, 0))
			if index < len(p.tableLines)-1 || finalHasNewline {
				p.raw("\n")
			}
		}
	} else {
		p.raw(renderTable(rows, alignments, p.width, p.styled))
	}
	p.tableLines = nil
	p.tableBytes, p.tableVerbatim = 0, false
	p.hadContent = true
}

// emitLiteralTail writes a leading-pipe candidate that never resolved, which the batch renders as
// the remaining lines joined and wrapped.
func (p *parser) emitLiteralTail() {
	stripped := p.tableHeld
	if info := ScanLine(p.tableHeld+"\n", false); info.NormalizeIndent {
		stripped = p.tableHeld[info.IndentLength:]
	}
	var raw strings.Builder
	raw.WriteString(stripped)
	if p.pending != "" {
		raw.WriteByte('\n')
		raw.WriteString(p.pending)
	}
	if p.sawNewline {
		raw.WriteByte('\n')
	}
	p.pending = ""
	p.mode = modeBlock
	p.startBlock()
	p.raw(wrapStyled(raw.String(), p.width, 0))
	p.hadContent = true
}

// startPipeLiteral renders a leading-pipe line that turned out not to open a table.
func (p *parser) startPipeLiteral() {
	line := p.tableHeld
	p.tableHeld = ""
	p.startBlock()
	if info := ScanLine(line+"\n", false); info.NormalizeIndent {
		line = line[info.IndentLength:]
	}
	p.raw(wrapStyled(renderInline(line, p.styled), p.width, 0))
	p.raw("\n")
	p.hadContent = true
}

// startBlockquote renders a blockquote line inline, keeping its marker as written.
func (p *parser) startBlockquote(line string, info LineInfo, complete bool) {
	p.startBlock()
	if info.NormalizeIndent {
		line = line[info.IndentLength:]
	}
	p.raw(wrapStyled(renderInline(line, p.styled), p.width, 0))
	if complete {
		p.raw("\n")
	}
	p.hadContent = true
}

// startHeading renders a heading and holds its tail until the next line says whether it was the
// document's last line.
func (p *parser) startHeading(line string, info LineInfo, complete bool) {
	p.startBlock()
	// The parser consumes the hashes and the single space that follows them, keeps any further
	// indentation, expands tabs, and does not strip a closing hash run.
	heading := line[info.IndentLength+info.MarkerLength:]
	if heading != "" && heading[0] == ' ' {
		heading = heading[1:]
	}
	heading = strings.ReplaceAll(heading, "\t", "    ")
	text, italicOpen := renderInlineWithDeferredItalicClose(heading, p.styled)
	if p.styled {
		headingOpen := terminaltheme.Open(terminaltheme.Heading)
		text = strings.ReplaceAll(text, ansiBoldOff, ansiBoldOff+headingOpen)
		if headingOpen != ansiBold {
			seenClose := make(map[string]struct{}, 2)
			for _, role := range []terminaltheme.Role{terminaltheme.CodeInline, terminaltheme.Link} {
				close := terminaltheme.Close(role)
				if close == "" {
					continue
				}
				if _, seen := seenClose[close]; seen {
					continue
				}
				seenClose[close] = struct{}{}
				text = strings.ReplaceAll(text, close, close+headingOpen)
			}
		}
		// The opening sequence is part of the payload, so a heading that is discarded before its
		// boundary leaves the terminal unstyled.
		text = headingOpen + text
	}
	p.headingSet = true
	p.headingText, p.headingItalic, p.headingNewline = text, italicOpen, complete
	p.afterHeading = true
	p.hadContent = true
}

// headingHeld reports whether a heading is waiting for the next line.
func (p *parser) headingHeld() bool {
	return p.headingSet
}

// emitHeading writes a held heading. last reports whether it ends the document, which is the only
// case where its italic close waits for the document end.
func (p *parser) emitHeading(last bool) {
	if !p.headingHeld() {
		return
	}
	p.raw(p.headingText)
	if p.styled {
		if p.headingItalic && !last {
			// The batch renders a heading that has a following line with the immediate inline
			// pass, so the italic close sits inside the text, before the heading's own close.
			p.raw(ansiItalicOff)
		}
		p.raw(terminaltheme.Close(terminaltheme.Heading))
		if p.headingItalic && last {
			p.deferredItalicClose = true
		}
	}
	if p.headingNewline {
		p.raw("\n")
	}
	p.headingSet = false
	p.headingText, p.headingItalic, p.headingNewline = "", false, false
}

// startThematic renders a divider. A `=` run stays literal; the other markers become spaced dots.
func (p *parser) startThematic(line string, info LineInfo, complete bool) {
	p.startBlock()
	if info.Marker == '=' {
		p.raw(wrapStyled(line, p.width, 0))
		if complete {
			p.raw("\n")
		}
		p.hadContent = true
		return
	}
	dots := 3
	for p.width > 0 && dots > 1 && dots+(dots-1)*3 > p.width {
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
	if p.styled {
		dividerText = ansiDim + dividerText + ansiBoldOff
	}
	p.raw(dividerText)
	p.raw("\n")
	p.hadContent = true
}

// startListItem writes a list item's marker and streams its text like a paragraph with a hanging
// continuation indent.
func (p *parser) startListItem(line string, info LineInfo) {
	markerEnd := info.IndentLength + info.MarkerLength
	indent := strings.Repeat(" ", info.IndentLength)
	continuation := info.IndentLength + 2
	text := strings.TrimLeft(line[markerEnd:], " \t")
	var prefix string
	if info.Kind == LineBullet {
		prefix = "• "
	} else {
		prefix = line[info.IndentLength:markerEnd] + " "
		continuation = info.IndentLength + terminalwidth.Cells(line[info.IndentLength:markerEnd]) + 1
	}
	if p.styled {
		prefix = ansiDim + prefix + ansiBoldOff
	}
	p.startProse(text, continuation, true)
	p.itemEnd = true
	p.wrap.write(indent + prefix)
	p.flushWrap()
}

// blank collapses a run of blank lines into the two newlines that separate blocks.
func (p *parser) blank() {
	if p.hadContent {
		p.ensureNewlines(2)
		p.afterBlank = true
		p.afterHeading = false
	}
}

// openFence opens a fenced code block and copies its body through verbatim.
func (p *parser) openFence(info LineInfo) {
	p.startBlock()
	p.fenceLength = info.MarkerLength
	p.fenceBodySeen = false
	p.emptyFenceAtEnd = true
	p.fenceStyleOpen = p.styled
	p.fenceStyleWritten = false
	p.mode = modeFence
}

// openFenceStyle writes a fence's opening sequence once it has content to hold, so a fence that is
// discarded before its first body byte leaves nothing open.
func (p *parser) openFenceStyle() {
	if p.fenceStyleOpen && !p.fenceStyleWritten {
		p.raw(terminaltheme.Open(terminaltheme.CodeBlock))
		p.fenceStyleWritten = true
	}
}

// startBlock pays the separator a new block owes and records that content exists.
func (p *parser) startBlock() {
	if p.hadContent {
		if p.afterHeading {
			// The heading already wrote its terminating newline and occupies a row even when it
			// has no glyphs, so the blank line it owes is one more newline. Counting stripped
			// trailing newlines cannot see that row.
			p.raw("\n")
		} else {
			want := 1
			if p.afterBlank {
				want = 2
			}
			p.ensureNewlines(want)
		}
	}
	p.hadContent = true
	p.afterBlank, p.afterHeading = false, false
}

// ensureNewlines writes newlines until the output ends in count of them, ignoring the style
// sequences that may follow a break.
func (p *parser) ensureNewlines(count int) {
	if !p.hadContent {
		return
	}
	for p.trailingNewlines < count {
		p.raw("\n")
	}
}

// feedInline hands text to the resumable inline walker, keeping whatever it does not settle. Prose
// tabs are expanded first, as wrapStyled expands them before wrapping.
func (p *parser) feedInline(text string, final bool) {
	if p.err != nil {
		return
	}
	text = p.expandTabs(text)
	input := p.unsettled + text
	p.unsettled = ""
	consumed, err := p.inline.renderResumable(p.wrapped, input, final)
	if err != nil {
		p.err = err
		return
	}
	if consumed < len(input) {
		p.unsettled = input[consumed:]
	}
}

// wrapped routes settled inline output through the block's wrapper. A non-positive width disables
// wrapping, and with it the wrapper's carriage-return drop, exactly as wrapStyled does.
func (p *parser) wrapped(text string) error {
	if p.width <= 0 {
		p.raw(text)
		return p.err
	}
	p.wrap.write(text)
	p.flushWrap()
	return p.err
}

// raw writes bytes that are not wrapped, such as a heading or a fence body.
func (p *parser) raw(text string) {
	if p.err != nil || text == "" {
		return
	}
	p.emit(text)
}

// emit writes bytes to the caller and tracks the trailing newline run the separator rules count.
// Counting happens here, after wrapping, because that is the text the batch renderer counts.
func (p *parser) emit(text string) {
	p.countNewlines(text)
	if err := p.write(text); err != nil {
		p.err = err
	}
}

// flushWrap hands the wrapper's completed bytes to the caller.
func (p *parser) flushWrap() {
	if p.err != nil {
		return
	}
	if value := p.wrap.drain(); value != "" {
		p.emit(value)
	}
}

// countNewlines tracks the trailing newline run the separator rules count, on the text before
// wrapping: a style sequence does not end a run, any other byte does.
func (p *parser) countNewlines(text string) {
	for index := 0; index < len(text); {
		if text[index] == '\x1b' && index+1 < len(text) && text[index+1] == '[' {
			end := index + 2
			for end < len(text) && (text[end] < 0x40 || text[end] > 0x7e) {
				end++
			}
			if end < len(text) {
				index = end + 1
				continue
			}
		}
		if text[index] == '\n' {
			p.trailingNewlines++
		} else {
			p.trailingNewlines = 0
		}
		index++
	}
}
