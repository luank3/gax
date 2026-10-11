package markdown

import (
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Stream incrementally renders Markdown while retaining parser input that may still change the
// interpretation of the visible suffix. width <= 0 disables wrapping.
type Stream struct {
	width   int
	styled  bool
	write   func(string) error
	input   strings.Builder
	emitted string
	err     error
	closed  bool
}

// NewStream creates an incremental Markdown renderer. A nil writer discards output.
func NewStream(width int, styled bool, write func(string) error) *Stream {
	return &Stream{width: width, styled: styled, write: write}
}

// Feed adds a provider chunk. Plain unwrapped prose is emitted eagerly; syntax whose meaning can
// still change and wrapped paragraphs are retained until a boundary or Close.
func (stream *Stream) Feed(chunk string) error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	stream.input.WriteString(chunk)
	stream.emitEagerPlainPrefix()
	if stream.err != nil {
		return stream.err
	}
	if stream.partialTableOverflow() {
		return stream.flushSegment()
	}
	if stream.tableEnded() {
		return stream.flushSegment()
	}
	return nil
}

// Boundary resolves buffered syntax and starts a fresh Markdown segment.
func (stream *Stream) Boundary() error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	return stream.flushSegment()
}

// SetStyled changes inline presentation mode at a segment boundary. Pending input is resolved
// using the old mode before later input is rendered with the new one.
func (stream *Stream) SetStyled(styled bool) error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	if stream.styled == styled {
		return nil
	}
	if err := stream.flushSegment(); err != nil {
		return err
	}
	stream.styled = styled
	return nil
}

// Reset discards unrendered parser input and prepares the stream for a retry.
func (stream *Stream) Reset() error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	stream.input.Reset()
	stream.emitted = ""
	return nil
}

// Close resolves the remaining input once. Repeated closes are harmless.
func (stream *Stream) Close() error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return nil
	}
	if err := stream.flushSegment(); err != nil {
		return err
	}
	stream.closed = true
	return nil
}

// InTable reports whether the current input ends inside a buffered leading-pipe table.
func (stream *Stream) InTable() bool {
	if stream == nil {
		return false
	}
	input := stream.input.String()
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) < 2 {
		return false
	}
	for start := 0; start+1 < len(lines); {
		_, _, end, ok := parseTable(lines, start, strings.HasSuffix(input, "\n"))
		if ok && end == len(lines)-1 {
			return true
		}
		if ok {
			start = end + 1
		} else {
			start++
		}
	}
	return false
}

func (stream *Stream) emitEagerPlainPrefix() {
	input := stream.input.String()
	if input == "" {
		return
	}
	end := 0
	pendingSoftBreak := -1
	lineStart := true
	for index := 0; index < len(input); {
		if !utf8.FullRuneInString(input[index:]) {
			break
		}
		r, size := utf8.DecodeRuneInString(input[index:])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if r == '\r' {
			if index+size < len(input) && input[index+size] == '\n' {
				index += size
				continue
			}
			break
		}
		if r == '\n' {
			if pendingSoftBreak >= 0 || stream.trailingHardBreak(input, index) || stream.hasDefiniteBlockAfter(input, index) {
				if pendingSoftBreak >= 0 {
					end = pendingSoftBreak + 1
				} else {
					end = index + size
				}
				break
			}
			pendingSoftBreak = index
			lineStart = true
			index += size
			continue
		}
		if lineStart && (r == ' ' || r == '\t') {
			index += size
			continue
		}
		if stream.unsafeEagerRune(input, index, r, lineStart) {
			break
		}
		if pendingSoftBreak >= 0 {
			end = pendingSoftBreak + 1
			pendingSoftBreak = -1
		}
		index += size
		end = index
		lineStart = false
	}
	if end == 0 {
		return
	}
	prefix := input[:end]
	rendered := renderDocument(prefix, stream.width, stream.styled)
	if rendered == "" {
		return
	}
	if !strings.HasPrefix(rendered, stream.emitted) {
		return
	}
	stream.emit(rendered[len(stream.emitted):])
}

func (stream *Stream) unsafeEagerRune(input string, index int, value rune, lineStart bool) bool {
	if strings.ContainsRune("*_`|#>—", value) {
		return true
	}
	if lineStart {
		if value == '-' || value == '+' || value >= '0' && value <= '9' {
			return true
		}
	}
	return (value == 'h' || value == 'H') && mightStartURL(input[index:])
}

func (stream *Stream) hasDefiniteBlockAfter(input string, newline int) bool {
	start := newline + 1
	if start >= len(input) {
		return false
	}
	lineEnd := strings.IndexByte(input[start:], '\n')
	hasNewline := lineEnd >= 0
	if hasNewline {
		lineEnd += start
	} else {
		lineEnd = len(input)
	}
	line := input[start:lineEnd]
	lineForScan := line
	if hasNewline {
		lineForScan += "\n"
	}
	info := ScanLine(lineForScan, false)
	switch info.Kind {
	case LineBlank, LineHeading, LineFence, LineThematic, LineBlockquote, LinePipe:
		return true
	case LineBullet, LineOrdered:
		return info.IndentLength <= 3
	default:
		return false
	}
}

func (stream *Stream) trailingHardBreak(input string, newline int) bool {
	lineStart := strings.LastIndexByte(input[:newline], '\n') + 1
	line := strings.TrimSuffix(input[lineStart:newline], "\r")
	spaces := len(line) - len(strings.TrimRight(line, " "))
	return spaces >= 2
}

func mightStartURL(value string) bool {
	value = strings.ToLower(value)
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(scheme, value) || strings.HasPrefix(value, scheme) {
			return true
		}
	}
	return false
}

func (stream *Stream) tableEnded() bool {
	input := stream.input.String()
	if !strings.HasSuffix(input, "\n") {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) < 3 {
		return false
	}
	for start := 0; start+1 < len(lines); {
		_, _, end, ok := parseTable(lines, start, strings.HasSuffix(input, "\n"))
		if ok {
			return end < len(lines)-1
		}
		start++
	}
	return false
}

func (stream *Stream) partialTableOverflow() bool {
	input := stream.input.String()
	if input == "" || strings.HasSuffix(input, "\n") {
		return false
	}
	lines := strings.Split(input, "\n")
	for start := range lines {
		if !strings.HasPrefix(strings.TrimSpace(lines[start]), "|") {
			continue
		}
		_, _, end, ok := parseTable(lines, start, false)
		if !ok || end != len(lines)-1 {
			continue
		}
		bufferedBytes := 0
		for index := start; index <= end; index++ {
			bufferedBytes += len(lines[index])
			if index < end {
				bufferedBytes++
			}
		}
		if bufferedBytes > maxTableBytes {
			return true
		}
	}
	return false
}

func (stream *Stream) flushSegment() error {
	input := stream.input.String()
	if input == "" {
		return nil
	}
	rendered := renderDocument(input, stream.width, stream.styled)
	if !strings.HasPrefix(rendered, stream.emitted) {
		return errors.New("markdown stream output changed after it was emitted")
	}
	stream.emit(rendered[len(stream.emitted):])
	if stream.err != nil {
		return stream.err
	}
	stream.input.Reset()
	stream.emitted = ""
	return nil
}

func (stream *Stream) emit(chunk string) {
	if chunk == "" || stream.err != nil {
		return
	}
	if stream.write != nil {
		if err := stream.write(chunk); err != nil {
			stream.err = err
			return
		}
	}
	stream.emitted += chunk
}

// Render renders one complete Markdown document through the same stream API used by live output.
func Render(input string, width int, styled bool) string {
	var output strings.Builder
	stream := NewStream(width, styled, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	_ = stream.Feed(input)
	_ = stream.Close()
	return output.String()
}
