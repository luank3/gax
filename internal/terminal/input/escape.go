package input

// Action is a decoded terminal editing action. An unknown or incomplete escape sequence maps to
// ActionNone after all bytes belonging to the sequence have been consumed.
type Action uint8

const (
	ActionNone Action = iota
	ActionMoveLeft
	ActionMoveRight
	ActionMoveWordLeft
	ActionMoveWordRight
	ActionLineStart
	ActionLineEnd
	ActionDeleteForward
	ActionHistoryPrev
	ActionHistoryNext
	ActionPageUp
	ActionPageDown
	ActionKillWordForward
	ActionKillWordBackAlnum
	ActionInsertNewline
	ActionPasteBegin
)

// ByteReader returns the next byte or a negative value when no byte is available.
type ByteReader func() int

// DecodeEscape decodes the bytes after an already-consumed ESC. It recognizes CSI, SS3, xterm
// modifier, rxvt lowercase-arrow, iTerm Meta-prefix, and Alt-letter forms used by the editor.
func DecodeEscape(read ByteReader) Action {
	if read == nil {
		return ActionNone
	}
	value := read()
	if value < 0 {
		return ActionNone
	}
	meta := false
	for strip := 0; value == 0x1b && strip < 4; strip++ {
		value = read()
		if value < 0 {
			return ActionNone
		}
		meta = true
	}
	if value == 0x1b {
		return ActionNone
	}
	if value == '[' {
		sequence, ok := readEscapeSequence(read, 32)
		if !ok {
			return ActionNone
		}
		if len(sequence) == 1 {
			final := sequence[0]
			if final >= 'a' && final <= 'd' {
				final -= 'a' - 'A'
			}
			return arrowAction(final, meta)
		}
		if string(sequence) == "200~" {
			return ActionPasteBegin
		}
		final := sequence[len(sequence)-1]
		if (final == '~' || final == '^' || final == '$' || final == '@') &&
			(len(sequence) == 2 || len(sequence) >= 4 && sequence[1] == ';') {
			switch sequence[0] {
			case '1', '7':
				return ActionLineStart
			case '4', '8':
				return ActionLineEnd
			case '3':
				return ActionDeleteForward
			case '5':
				return ActionPageUp
			case '6':
				return ActionPageDown
			}
		}
		if len(sequence) >= 4 && sequence[0] == '1' && sequence[1] == ';' {
			modifier := parseXtermModifier(sequence)
			return arrowAction(final, xtermModifierMovesWord(modifier) || meta)
		}
		return ActionNone
	}
	if value == 'O' {
		sequence, ok := readEscapeSequence(read, 32)
		if !ok {
			return ActionNone
		}
		final := sequence[len(sequence)-1]
		word := false
		if final >= 'a' && final <= 'd' {
			final -= 'a' - 'A'
			word = true
		} else {
			word = xtermModifierMovesWord(parseXtermModifier(sequence))
		}
		return arrowAction(final, word || meta)
	}
	if value == '\r' || value == '\n' {
		return ActionInsertNewline
	}
	switch value {
	case 'b', 'B':
		return ActionMoveWordLeft
	case 'f', 'F':
		return ActionMoveWordRight
	case 'd', 'D':
		return ActionKillWordForward
	case 0x7f, 0x08:
		return ActionKillWordBackAlnum
	default:
		return ActionNone
	}
}

func readEscapeSequence(read ByteReader, capacity int) ([]byte, bool) {
	sequence := make([]byte, 0, capacity-1)
	overflowed := false
	for count := 0; count < 64; count++ {
		value := read()
		if value < 0 {
			return nil, false
		}
		if len(sequence)+1 < capacity {
			sequence = append(sequence, byte(value))
		} else {
			overflowed = true
		}
		if value >= 0x40 && value <= 0x7e || value == '$' {
			return sequence, !overflowed
		}
	}
	return nil, false
}

func parseXtermModifier(sequence []byte) int {
	if len(sequence) < 4 || sequence[0] != '1' || sequence[1] != ';' {
		return 1
	}
	modifier := 0
	for _, value := range sequence[2 : len(sequence)-1] {
		if value == ';' {
			break
		}
		if value < '0' || value > '9' {
			return 1
		}
		modifier = modifier*10 + int(value-'0')
		if modifier > 99 {
			return 1
		}
	}
	if modifier == 0 {
		return 1
	}
	return modifier
}

func xtermModifierMovesWord(modifier int) bool {
	return modifier > 0 && (modifier-1)&0x0e != 0
}

func arrowAction(final byte, word bool) Action {
	switch final {
	case 'A':
		return ActionHistoryPrev
	case 'B':
		return ActionHistoryNext
	case 'C':
		if word {
			return ActionMoveWordRight
		}
		return ActionMoveRight
	case 'D':
		if word {
			return ActionMoveWordLeft
		}
		return ActionMoveLeft
	case 'H':
		return ActionLineStart
	case 'F':
		return ActionLineEnd
	default:
		return ActionNone
	}
}
