package markdown

import (
	"strings"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

// renderResumable consumes the longest prefix of input whose rendering is already final, writing
// through emit, and returns the number of bytes consumed. With final set it consumes everything
// and reproduces the batch renderer exactly.
//
// This is the streaming counterpart of a whole-string inline pass: same constructs and same state,
// but it stops wherever a later byte could change the meaning instead of walking to the end.
// Stopping is what lets a paragraph emit prose as it arrives without re-rendering, so streaming
// stays linear and chunked output equals a whole-document render.
func (state *inlineState) renderResumable(emit func(string) error, input string, final bool) (int, error) {
	consumed, err := state.renderWindow(emit, input, final)
	if consumed > 0 {
		state.prevByte = input[consumed-1]
	}
	return consumed, err
}

// leftByte is the byte to the left of index, falling back to the byte before this window. The
// emphasis and link rules read it, and a chunk boundary must not look like the document start.
func (state *inlineState) leftByte(input string, index int) byte {
	if index > 0 {
		return input[index-1]
	}
	return state.prevByte
}

func (state *inlineState) renderWindow(emit func(string) error, input string, final bool) (int, error) {
	index := 0
	for index < len(input) {
		if input[index] == inlineLineBoundary {
			index++
			continue
		}

		if input[index] == inlineLiteralTail {
			// The rest of this window is literal: copy it without inline parsing so an open run
			// still closes after it.
			state.pendingDash = false
			if err := emit(input[index+1:]); err != nil {
				return index, err
			}
			state.previousSpace = input[len(input)-1] == ' '
			state.hasPrevious = true
			return len(input), nil
		}

		if input[index] == emDash[0] {
			if strings.HasPrefix(input[index:], emDash) {
				if state.hasPrevious && !state.previousSpace {
					if err := emit(" "); err != nil {
						return index, err
					}
				}
				if err := emit(emDash); err != nil {
					return index, err
				}
				state.previousSpace, state.hasPrevious, state.pendingDash = false, true, true
				index += len(emDash)
				continue
			}
			if !final && strings.HasPrefix(emDash, input[index:]) {
				return index, nil
			}
		}

		// A URL scheme is only resolved once the body after it is known: a complete scheme whose
		// body is still empty, trims away, or runs to the buffer end may yet become a link, so the
		// position waits for the next byte rather than leaking the scheme as literal text. A scheme
		// preceded by a word character can never be a link, so it falls through immediately.
		completeScheme, partialScheme := schemeMatch(input[index:])
		if completeScheme && !isASCIIAlnum(state.leftByte(input, index)) {
			if end, ok := bareURL(input, index); ok && (final || !urlBodyReachesEnd(input, end)) {
				if err := state.emitLink(emit, input[index:end]); err != nil {
					return index, err
				}
				index = end
				continue
			}
			if !final {
				return index, nil
			}
		} else if partialScheme && !final {
			return index, nil
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
				if err := state.emitInlineCode(emit, input[contentStart:contentEnd]); err != nil {
					return index, err
				}
				index = contentEnd + 1
				continue
			}
			if boundary >= 0 {
				if err := state.emitInlineCode(emit, input[contentStart:searchEnd]); err != nil {
					return index, err
				}
				index = searchEnd + 1
				continue
			}
			if !final {
				// The closing run may still arrive in a later chunk.
				return index, nil
			}
			// An unterminated inline-code opener consumes the rest of this inline segment,
			// matching the streaming parser's EOF flush.
			if err := state.emitInlineCode(emit, input[contentStart:]); err != nil {
				return index, err
			}
			return len(input), nil
		}

		if state.unmatchedItalic && (input[index] == '_' ||
			input[index] == '*' && (index+1 >= len(input) || input[index+1] != '*')) {
			if state.styled {
				if err := emit(ansiItalicOff); err != nil {
					return index, err
				}
			}
			state.unmatchedItalic = false
			index++
			continue
		}

		if input[index] == '_' && index+1 < len(input) && input[index+1] == '_' {
			if canOpenEmphasis(state.leftByte(input, index), input[index+1]) {
				// The upstream parser processes each underscore independently: the first
				// opens italic and the adjacent second underscore closes it immediately.
				if state.styled {
					if err := emit(ansiItalic + ansiItalicOff); err != nil {
						return index, err
					}
				}
				index += 2
				continue
			}
		}

		marker, open, close := "", "", ""
		if index+1 < len(input) && input[index:index+2] == "**" {
			marker, open, close = input[index:index+2], ansiBold, ansiBoldOff
		} else if input[index] == '*' || input[index] == '_' {
			if index+1 >= len(input) && !final {
				// A single marker may still be the first byte of a bold run.
				return index, nil
			}
			marker, open, close = input[index:index+1], ansiItalic, ansiItalicOff
		}
		if marker != "" {
			right := index + len(marker)
			left := state.leftByte(input, index)
			if right >= len(input) && !final {
				// The byte after the marker decides whether it can open at all.
				return index, nil
			}
			if right < len(input) && canOpenEmphasis(left, input[right]) {
				region := input[right:]
				closeAt := strings.Index(region, marker)
				if len(marker) == 1 {
					closeAt = findItalicCloser(region)
				}
				if closeAt >= 0 && !final && !emphasisCloserSettled(region, closeAt) {
					// The scan reads the byte after each candidate and skips past a URL it
					// recognizes, so a closer at the very end of the buffer, or a region holding a
					// URL that may extend past it, can still be replaced by a later one.
					return index, nil
				}
				if closeAt == 0 && len(marker) == 1 && input[right] != marker[0] &&
					(input[right] == '*' || input[right] == '_') {
					if state.styled {
						if err := emit(ansiItalic + ansiItalicOff); err != nil {
							return index, err
						}
					}
					index = right + 1
					continue
				}
				if closeAt > 0 {
					contentEnd := right + closeAt
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						if err := emit(open); err != nil {
							return index, err
						}
					}
					if err := state.renderSubRegion(emit, input[right:contentEnd]); err != nil {
						return index, err
					}
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						if err := emit(close); err != nil {
							return index, err
						}
					}
					index = contentEnd + len(marker)
					continue
				}
				if closeAt < 0 {
					if !final {
						// The closing run may still arrive in a later chunk.
						return index, nil
					}
					if state.styled && !(state.suppressBold && len(marker) == 2) {
						if err := emit(open); err != nil {
							return index, err
						}
					}
					unmatchedItalic := len(marker) == 1
					if unmatchedItalic {
						state.unmatchedItalic = true
					}
					if err := state.renderSubRegion(emit, input[right:]); err != nil {
						return index, err
					}
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
						if err := emit(close); err != nil {
							return index, err
						}
					}
					return len(input), nil
				}
			}
			if len(marker) == 2 {
				if err := state.writeTextTo(emit, marker); err != nil {
					return index, err
				}
				index += 2
				continue
			}
		}

		// Trailing whitespace and a trailing carriage return both take their meaning from the byte
		// that follows, so they wait for it.
		if !final && index+1 == len(input) && (input[index] == '\r' || input[index] == ' ' || input[index] == '\t') {
			return index, nil
		}

		_, size := utf8.DecodeRuneInString(input[index:])
		if size < 1 {
			size = 1
		}
		if err := state.writeTextTo(emit, input[index:index+size]); err != nil {
			return index, err
		}
		index += size
	}
	return index, nil
}

// emphasisCloserSettled reports whether a closer found at closeAt stays the closer once more input
// arrives.
func emphasisCloserSettled(region string, closeAt int) bool {
	if closeAt+1 >= len(region) {
		return false
	}
	return !strings.Contains(strings.ToLower(region[:closeAt+1]), "http")
}

// renderSubRegion renders a bounded region the way the batch recursion does: the region start is
// its own left edge, so the enclosing walk's neighbour byte is restored afterwards.
func (state *inlineState) renderSubRegion(emit func(string) error, region string) error {
	saved := state.prevByte
	state.prevByte = 0
	_, err := state.renderResumable(emit, region, true)
	state.prevByte = saved
	return err
}

func (state *inlineState) emitInlineCode(emit func(string) error, content string) error {
	if state.styled {
		if err := emit(terminaltheme.Open(terminaltheme.CodeInline)); err != nil {
			return err
		}
	}
	if err := state.writeTextTo(emit, content); err != nil {
		return err
	}
	if state.styled {
		return emit(terminaltheme.Close(terminaltheme.CodeInline))
	}
	return nil
}

// emitLink writes one bare URL with its link styling and any pending em-dash space.
func (state *inlineState) emitLink(emit func(string) error, url string) error {
	if state.pendingDash {
		state.pendingDash = false
		if err := emit(" "); err != nil {
			return err
		}
	}
	if state.styled {
		if err := emit(terminaltheme.Open(terminaltheme.Link)); err != nil {
			return err
		}
	}
	if err := state.writeTextTo(emit, url); err != nil {
		return err
	}
	if state.styled {
		return emit(terminaltheme.Close(terminaltheme.Link))
	}
	return nil
}

// schemeMatch reports whether value starts with a URL scheme, or is still a prefix of one. Only the
// scheme length is examined, so the call stays constant-time on long input.
func schemeMatch(value string) (complete, partial bool) {
	if value == "" || value[0] != 'h' && value[0] != 'H' {
		return false, false
	}
	const maxScheme = len("https://")
	if len(value) > maxScheme {
		value = value[:maxScheme]
	}
	lower := strings.ToLower(value)
	for _, scheme := range [...]string{"https://", "http://"} {
		if strings.HasPrefix(lower, scheme) {
			return true, false
		}
		if strings.HasPrefix(scheme, lower) {
			return false, true
		}
	}
	return false, false
}

// urlBodyReachesEnd reports whether every byte from the given offset is URL body content, which
// means the link scan ran to the end of the buffered input rather than stopping at a terminator.
func urlBodyReachesEnd(input string, from int) bool {
	for index := from; index < len(input); index++ {
		switch input[index] {
		case ' ', '\t', '\n', '\r', '<', '>', '`', inlineLineBoundary:
			return false
		}
	}
	return true
}

// writeTextTo is the emitting counterpart of inlineState.writeText.
func (state *inlineState) writeTextTo(emit func(string) error, text string) error {
	if text == "" {
		return nil
	}
	if state.pendingDash {
		state.pendingDash = false
		if !isMarkdownSpace(text[0]) {
			if err := emit(" "); err != nil {
				return err
			}
		}
	}
	if err := emit(text); err != nil {
		return err
	}
	state.previousSpace = isMarkdownSpace(text[len(text)-1])
	state.hasPrevious = true
	return nil
}
