package markdown

import (
	"io"
	"strings"
)

// Stream incrementally renders Markdown as chunks arrive.
//
// It is a thin adapter over the block parser: Feed hands input to the parser, which emits
// everything a later byte cannot change and holds the rest. A boundary or a styled-mode change
// resolves the held bytes against end of segment and starts fresh, the same way a flush or a
// styled-mode change does.
type Stream struct {
	width  int
	styled bool
	write  func(string) error
	block  *parser
	err    error
	closed bool
}

// NewStream creates an incremental Markdown renderer. A nil writer discards output.
func NewStream(width int, styled bool, write func(string) error) *Stream {
	return &Stream{width: width, styled: styled, write: write}
}

// Feed adds a provider chunk. Prose is emitted as it settles; syntax whose meaning can still
// change is retained until the bytes that decide it arrive.
func (stream *Stream) Feed(chunk string) error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	stream.parser().feed(chunk, false)
	return stream.failure()
}

// Boundary resolves buffered syntax and starts a fresh Markdown segment.
func (stream *Stream) Boundary() error {
	if stream.err != nil {
		return stream.err
	}
	if stream.closed {
		return io.ErrClosedPipe
	}
	stream.parser().feed("", true)
	if err := stream.failure(); err != nil {
		return err
	}
	stream.block = nil
	return nil
}

// SetStyled changes inline presentation mode at a segment boundary. Pending input is resolved
// under the old mode, so a style switch never reinterprets bytes already written.
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
	if err := stream.Boundary(); err != nil {
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
	if stream.block != nil {
		// The parser may have written a style sequence it has not closed yet.
		stream.block.discard()
	}
	stream.block = nil
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
	stream.parser().feed("", true)
	stream.closed = true
	return stream.failure()
}

// InTable reports whether the input currently ends inside a collecting table, which the agent
// surfaces as a spinner while the table accumulates silently.
func (stream *Stream) InTable() bool {
	if stream == nil || stream.block == nil {
		return false
	}
	if stream.block.mode == modeTableStart {
		// A leading-pipe line whose delimiter row is still arriving is a table attempt in
		// progress, which is what the agent's spinner reports.
		return true
	}
	if stream.block.mode != modeTable {
		return false
	}
	// A line that has arrived without a pipe cannot become a row, so the attempt is over for the
	// spinner even though the bytes are only decided once the line completes.
	return stream.block.pending == "" || strings.Contains(stream.block.pending, "|")
}

// parser lazily creates the block parser, since a reset discards it.
func (stream *Stream) parser() *parser {
	if stream.block == nil {
		stream.block = newParser(stream.width, stream.styled, stream.write)
	}
	return stream.block
}

// failure reports the parser's write error, if any.
func (stream *Stream) failure() error {
	if stream.block != nil && stream.block.err != nil {
		stream.err = stream.block.err
	}
	return stream.err
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
