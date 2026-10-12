package input

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

const (
	EscapeTimeout = 50 * time.Millisecond
	pasteTimeout  = 5 * time.Second
	maxPasteBytes = 1 << 20
)

// ByteSource supplies terminal bytes. A timed-out read returns available=false and a nil error;
// an exhausted source returns io.EOF.
type ByteSource interface {
	ReadByteTimeout(timeout time.Duration) (value byte, available bool, err error)
}

// ByteReaderFunc adapts a function to ByteSource.
type ByteReaderFunc func(timeout time.Duration) (value byte, available bool, err error)

func (read ByteReaderFunc) ReadByteTimeout(timeout time.Duration) (byte, bool, error) {
	return read(timeout)
}

// Completer matches a buffer span at the cursor and optionally replaces it or supplies ghost
// candidates when a repeated Tab has nothing more to insert.
type Completer struct {
	Match      func(buffer string, cursor int) (start, end int, matched bool)
	Complete   func(token string) (replacement string, ok bool)
	Candidates func(token string) (text string, ok bool)
	Modal      bool // Run Complete through the TTY reader's cooked-mode callback.
}

// EditorOptions adds application-specific completion and hint callbacks to the terminal editor.
type EditorOptions struct {
	Completers  []Completer
	ModalKeys   map[byte]func()
	InitialText string
	EmptySubmit bool
	// EditBuffer runs in cooked mode and returns an edited replacement when ok is true.
	EditBuffer     func(buffer string) (text string, ok bool)
	Hint           func(buffer string) (text string, ok bool)
	DisplayColumns func(terminalColumns int) int
	PasteHook      func() (text string, ok bool)
	PasteFilter    func(text string) (replacement string, ok bool)
}

// Editor reads and repaints one line using Core for editing state. Its injected byte source lets
// the key loop run in tests without a terminal device.
type Editor struct {
	Core                 *Core
	History              *HistoryFile
	InitialText          string
	EmptySubmit          bool
	Prompt               string
	Reader               ByteSource
	Writer               io.Writer
	Columns              int
	Rows                 int
	Context              context.Context
	Completers           []Completer
	ModalKeys            map[byte]func()
	EditBuffer           func(buffer string) (text string, ok bool)
	Hint                 func(buffer string) (text string, ok bool)
	PasteHook            func() (text string, ok bool)
	PasteFilter          func(text string) (replacement string, ok bool)
	terminalSize         func() (columns, rows int)
	modalRun             func(func() (string, bool)) (string, bool, error)
	pending              []byte
	candidates           string
	lastKeyWasTab        bool
	exitArmed            bool
	ghostPainted         bool
	paintedCursorRow     int
	paintedRows          int
	previousPaintClipped bool
	windowTop            int
	topIndicatorWidth    int
}

// ReadLine returns submitted=false on empty EOF or the editor's double-Ctrl-C exit gesture. A
// non-empty buffer is returned at EOF; empty Enter is ignored unless the app supplies a placeholder.
func (editor *Editor) ReadLine() (line string, submitted bool, err error) {
	if editor == nil || editor.Reader == nil {
		return "", false, errors.New("input editor has no byte source")
	}
	if editor.Core == nil {
		editor.Core = NewCore()
	}
	if editor.Writer == nil {
		editor.Writer = io.Discard
	}
	if editor.Columns <= 0 {
		editor.Columns = 80
	}
	editor.pending = nil
	editor.candidates = ""
	editor.lastKeyWasTab = false
	editor.exitArmed = false
	editor.ghostPainted = false
	editor.Core.SetBuffer(editor.InitialText)
	editor.Core.ResetHistoryNavigation()
	exitArmed := false
	if err := editor.repaint(); err != nil {
		return "", false, err
	}

	for {
		value, available, readErr := editor.readEditorKey()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if text := editor.Core.Text(); text != "" {
					line = strings.ToValidUTF8(text, "\uFFFD")
					if err := editor.leaveEditArea(); err != nil {
						return "", false, err
					}
					return line, true, nil
				}
				if err := editor.leaveEditArea(); err != nil {
					return "", false, err
				}
				return "", false, nil
			}
			_ = editor.leaveEditArea()
			return "", false, readErr
		}
		if !available {
			continue
		}
		if value != 0x03 {
			exitArmed = false
		}
		editor.exitArmed = exitArmed
		if value != 0x09 {
			editor.lastKeyWasTab = false
		}
		editor.candidates = ""
		if callback := editor.ModalKeys[value]; callback != nil {
			if editor.modalRun != nil {
				if _, _, err := editor.modalRun(func() (string, bool) {
					callback()
					return "", false
				}); err != nil {
					_ = editor.leaveEditArea()
					return "", false, err
				}
			} else {
				callback()
			}
			if err := editor.repaint(); err != nil {
				return "", false, err
			}
			continue
		}
		changed := true
		switch value {
		case 0x01: // Ctrl-A
			editor.Core.SetCursor(editor.Core.LineStart())
		case 0x02: // Ctrl-B
			editor.Core.MoveLeft()
		case 0x03: // Ctrl-C clears a draft; twice on an empty line exits.
			if editor.Core.Text() != "" {
				editor.addHistory(editor.Core.Text(), true)
				editor.Core.SetBuffer("")
			} else if exitArmed {
				if err := editor.leaveEditArea(); err != nil {
					return "", false, err
				}
				return "", false, nil
			} else {
				exitArmed = true
			}
			editor.exitArmed = exitArmed
		case 0x04: // Ctrl-D deletes forward or reports EOF on an empty buffer.
			if editor.Core.Text() == "" {
				if err := editor.leaveEditArea(); err != nil {
					return "", false, err
				}
				return "", false, nil
			}
			editor.Core.DeleteForward()
		case 0x05: // Ctrl-E
			editor.Core.SetCursor(editor.Core.LineEnd())
		case 0x06: // Ctrl-F
			editor.Core.MoveRight()
		case 0x07: // Ctrl-G opens the current buffer in the configured external editor.
			if editor.EditBuffer != nil {
				text, ok, editErr := editor.editBuffer()
				if editErr != nil {
					return "", false, editErr
				}
				if ok {
					editor.Core.SetBuffer(text)
				}
			}
		case 0x08, 0x7f: // Ctrl-H and DEL/backspace
			editor.Core.DeleteBack()
		case 0x09: // Tab tries registered completions and may show candidates on a repeat.
			if err := editor.completeAtCursor(); err != nil {
				return "", false, err
			}
		case 0x0a: // Shift+Enter
			editor.Core.Insert("\n")
		case 0x0b: // Ctrl-K
			editor.Core.KillToEndOfLine()
		case 0x0c: // Ctrl-L
			if _, err := io.WriteString(editor.Writer, "\x1b[2J\x1b[H"); err != nil {
				return "", false, err
			}
			editor.paintedCursorRow = 0
			editor.paintedRows = 0
			editor.previousPaintClipped = false
			editor.windowTop = 0
			editor.ghostPainted = false
		case 0x0d: // Enter
			if editor.Core.Text() == "" && !editor.EmptySubmit {
				changed = false
				break
			}
			line = strings.ToValidUTF8(editor.Core.Text(), "\uFFFD")
			if line == "" {
				if err := editor.eraseEditArea(); err != nil {
					return "", false, err
				}
			} else if err := editor.renderSubmitted(line); err != nil {
				return "", false, err
			}
			return line, true, nil
		case 0x0e: // Ctrl-N
			editor.Core.HistoryNext()
		case 0x10: // Ctrl-P
			editor.Core.HistoryPrev()
		case 0x12: // Ctrl-R: incremental reverse history search.
			submit, searchErr := editor.searchHistory()
			if searchErr != nil {
				_ = editor.leaveEditArea()
				return "", false, searchErr
			}
			if submit && editor.Core.Text() != "" {
				line = strings.ToValidUTF8(editor.Core.Text(), "\uFFFD")
				if err := editor.renderSubmitted(line); err != nil {
					return "", false, err
				}
				return line, true, nil
			}
		case 0x15: // Ctrl-U
			editor.Core.KillToStartOfLine()
		case 0x16: // Ctrl-V inserts text returned by the application clipboard hook.
			editor.invokePasteHook()
		case 0x17: // Ctrl-W
			editor.Core.KillWordBack()
		case 0x1b:
			action, escapeErr := editor.readEscapeAction()
			if escapeErr != nil {
				_ = editor.leaveEditArea()
				return "", false, escapeErr
			}
			changed = editor.applyAction(action)
		default:
			if value >= 0x20 {
				sequence, sequenceErr := editor.readUTF8Sequence(value)
				if sequenceErr != nil {
					_ = editor.leaveEditArea()
					return "", false, sequenceErr
				}
				editor.Core.Insert(string(sequence))
			} else {
				changed = false
			}
		}
		if changed {
			if err := editor.repaint(); err != nil {
				return "", false, err
			}
		}
	}
}

func (editor *Editor) leaveEditArea() error {
	var frame bytes.Buffer
	down := editor.paintedRows - 1 - editor.paintedCursorRow
	if down > 0 {
		fmt.Fprintf(&frame, "\x1b[%dB", down)
	}
	if editor.ghostPainted {
		frame.WriteString("\x1b[K")
	}
	frame.WriteString("\r\n")
	err := writeAll(editor.Writer, frame.Bytes())
	editor.paintedCursorRow = 0
	editor.paintedRows = 0
	editor.previousPaintClipped = false
	editor.windowTop = 0
	editor.topIndicatorWidth = 0
	editor.ghostPainted = false
	return err
}

func (editor *Editor) eraseEditArea() error {
	rows := editor.paintedRows
	if rows <= 0 {
		rows = 1
	}
	var frame bytes.Buffer
	if editor.paintedCursorRow > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", editor.paintedCursorRow)
	}
	frame.WriteByte('\r')
	for row := 0; row < rows; row++ {
		frame.WriteString(ansiEraseLine)
		if row+1 < rows {
			frame.WriteString("\x1b[1B")
		}
	}
	if rows > 1 {
		fmt.Fprintf(&frame, "\x1b[%dA", rows-1)
	}
	err := writeAll(editor.Writer, frame.Bytes())
	editor.paintedCursorRow = 0
	editor.paintedRows = 0
	editor.previousPaintClipped = false
	editor.windowTop = 0
	editor.topIndicatorWidth = 0
	editor.ghostPainted = false
	return err
}

// renderSubmitted replaces the editable prompt with the committed user-message display used by
// the REPL. Keeping this in the input package lets submission and terminal cleanup stay atomic.
func (editor *Editor) renderSubmitted(text string) error {
	if err := editor.eraseEditArea(); err != nil {
		return err
	}
	accentOpen := terminaltheme.Open(terminaltheme.Accent)
	accentClose := terminaltheme.Close(terminaltheme.Accent)
	if err := writeAll(editor.Writer, []byte(accentOpen+"▌ ")); err != nil {
		return err
	}
	core := NewCore()
	core.SetBuffer(text)
	columns := editor.Columns
	if columns <= 0 {
		columns = 80
	}
	var renderErr error
	core.Render(2, 2, columns, func(event RenderEvent) {
		if renderErr != nil {
			return
		}
		if event.Kind == RenderGlyph {
			renderErr = writeAll(editor.Writer, []byte(event.Bytes))
			return
		}
		renderErr = writeAll(editor.Writer, []byte(accentClose+"\x1b[K\r\n"+accentOpen+"▌ "))
	})
	if renderErr != nil {
		return renderErr
	}
	return writeAll(editor.Writer, []byte(accentClose+"\x1b[K\r\n"))
}

func (editor *Editor) completeAtCursor() error {
	text := editor.Core.Text()
	for _, completer := range editor.Completers {
		if completer.Match == nil {
			continue
		}
		start, end, matched := completer.Match(text, editor.Core.Cursor())
		if !matched {
			continue
		}
		if start >= 0 && start <= end && end <= len(text) {
			token := text[start:end]
			replacement, ok := "", false
			if completer.Complete != nil {
				if completer.Modal && editor.modalRun != nil {
					var err error
					replacement, ok, err = editor.modalRun(func() (string, bool) {
						return completer.Complete(token)
					})
					if err != nil {
						return err
					}
				} else {
					replacement, ok = completer.Complete(token)
				}
			}
			if ok && replacement != "" {
				editor.Core.ReplaceSpan(start, end, &replacement)
			} else if editor.lastKeyWasTab && completer.Candidates != nil {
				editor.candidates, _ = completer.Candidates(token)
			}
		}
		break
	}
	editor.lastKeyWasTab = true
	return nil
}

func (editor *Editor) editBuffer() (string, bool, error) {
	if editor.modalRun == nil {
		text, ok := editor.EditBuffer(editor.Core.Text())
		return text, ok, nil
	}
	return editor.modalRun(func() (string, bool) {
		return editor.EditBuffer(editor.Core.Text())
	})
}

func (editor *Editor) ghostText() string {
	if editor.Core.Cursor() != len(editor.Core.Text()) {
		return ""
	}
	if editor.exitArmed {
		return "ctrl+c again to exit"
	}
	if editor.candidates != "" {
		return editor.candidates
	}
	if editor.Hint != nil {
		if text, ok := editor.Hint(editor.Core.Text()); ok {
			return text
		}
	}
	return ""
}

func (editor *Editor) addHistory(line string, sessionOnly bool) {
	if editor.History != nil {
		if sessionOnly {
			editor.History.AddSession(editor.Core, line)
		} else {
			editor.History.Add(editor.Core, line)
		}
		return
	}
	editor.Core.AddHistory(line)
}

func (editor *Editor) readUTF8Sequence(first byte) ([]byte, error) {
	sequence := []byte{first}
	for need := utf8SequenceLength(first) - 1; need > 0; need-- {
		continuation, ok, err := editor.readTimedByte(EscapeTimeout)
		if err != nil {
			return nil, err
		}
		if !ok || continuation&0xc0 != 0x80 {
			if ok {
				// A malformed UTF-8 leader must not consume the next control or text key.
				editor.pending = append([]byte{continuation}, editor.pending...)
			}
			break
		}
		sequence = append(sequence, continuation)
	}
	return sequence, nil
}

func (editor *Editor) readEscapeAction() (Action, error) {
	action, err := editor.decodeEscapeAction()
	if err != nil {
		return ActionNone, err
	}
	if action == ActionPasteBegin {
		if err := editor.readPaste(); err != nil {
			return ActionNone, err
		}
	}
	return action, nil
}

func (editor *Editor) decodeEscapeAction() (Action, error) {
	var readErr error
	action := DecodeEscape(func() int {
		value, available, err := editor.readInputByte(EscapeTimeout)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			return -1
		}
		if !available {
			return -1
		}
		return int(value)
	})
	if readErr != nil {
		return ActionNone, readErr
	}
	return action, nil
}

func (editor *Editor) applyAction(action Action) bool {
	switch action {
	case ActionMoveLeft:
		editor.Core.MoveLeft()
	case ActionMoveRight:
		editor.Core.MoveRight()
	case ActionMoveWordLeft:
		editor.Core.MoveWordLeft()
	case ActionMoveWordRight:
		editor.Core.MoveWordRight()
	case ActionLineStart:
		editor.Core.SetCursor(editor.Core.LineStart())
	case ActionLineEnd:
		editor.Core.SetCursor(editor.Core.LineEnd())
	case ActionDeleteForward:
		editor.Core.DeleteForward()
	case ActionHistoryPrev:
		editor.Core.HistoryPrev()
	case ActionHistoryNext:
		editor.Core.HistoryNext()
	case ActionKillWordForward:
		editor.Core.KillWordForward()
	case ActionKillWordBackAlnum:
		editor.Core.KillWordBackAlnum()
	case ActionInsertNewline:
		editor.Core.Insert("\n")
	case ActionPasteBegin:
		// readPaste already inserted the body.
	default:
		return false
	}
	return true
}

func (editor *Editor) readTimedByte(timeout time.Duration) (byte, bool, error) {
	value, available, err := editor.readInputByte(timeout)
	if errors.Is(err, io.EOF) {
		return 0, false, nil
	}
	return value, available, err
}

func (editor *Editor) readEditorKey() (byte, bool, error) {
	value, available, err := editor.readInputByte(0)
	if err != nil || editor.terminalSize == nil {
		return value, available, err
	}
	columns, rows := editor.terminalSize()
	if editor.resize(columns, rows) {
		if err := editor.repaint(); err != nil {
			return 0, false, err
		}
	}
	return value, available, nil
}

func (editor *Editor) readInputByte(timeout time.Duration) (byte, bool, error) {
	if len(editor.pending) > 0 {
		value := editor.pending[0]
		editor.pending = editor.pending[1:]
		return value, true, nil
	}
	if editor.Context != nil && timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	value, available, err := editor.Reader.ReadByteTimeout(timeout)
	if err == nil && !available && editor.Context != nil {
		err = editor.Context.Err()
	}
	return value, available, err
}

func (editor *Editor) readPaste() error {
	body, err := editor.readPasteBody()
	if err != nil {
		return err
	}
	if body == "" {
		editor.invokePasteHook()
		return nil
	}
	editor.Core.CommitPaste(body, editor.PasteFilter)
	return nil
}

func (editor *Editor) invokePasteHook() {
	if editor.PasteHook == nil {
		return
	}
	text, ok := editor.PasteHook()
	if ok {
		editor.Core.Insert(text)
	}
}

func (editor *Editor) readPasteBody() (string, error) {
	marker := []byte("\x1b[201~")
	pending := make([]byte, 0, len(marker))
	body := make([]byte, 0, 256)
	swallowLF := false
	for {
		value, available, err := editor.readTimedByte(pasteTimeout)
		if err != nil {
			return "", err
		}
		if !available {
			break
		}
		if value == '\n' && swallowLF {
			swallowLF = false
			continue
		}
		if value == '\r' {
			value = '\n'
			swallowLF = true
		} else {
			swallowLF = false
		}
		if value == 0 {
			value = ' '
		}
		pending = append(pending, value)
		for len(pending) > 0 {
			prefixLen := len(pending)
			if prefixLen > len(marker) {
				prefixLen = len(marker)
			}
			if bytes.Equal(pending[:prefixLen], marker[:prefixLen]) {
				if len(pending) == len(marker) {
					return string(body), nil
				}
				break
			}
			if len(body) < maxPasteBytes {
				body = append(body, pending[0])
			}
			pending = pending[1:]
		}
	}
	// A timed-out or interrupted paste may leave marker-prefix bytes buffered. The ported behavior drops
	// these bytes rather than leaking a partial terminator into the editor.
	return string(body), nil
}

func (editor *Editor) searchHistory() (submit bool, err error) {
	original := editor.Core.Text()
	originalCursor := editor.Core.Cursor()
	originalHistoryPosition := editor.Core.HistoryPosition()
	originalPrompt := editor.Prompt
	query := make([]byte, 0, 32)
	match := -1
	direction := -1
	noMatch := false
	accepted := false

	for {
		showHistorySearchMatch(editor.Core, original, originalCursor, string(query), match, noMatch)
		editor.Prompt = buildHistorySearchPrompt(string(query), direction, noMatch, editor.Columns)
		if err := editor.repaint(); err != nil {
			editor.Prompt = originalPrompt
			restoreHistorySearchDraft(editor.Core, original, originalCursor)
			return false, err
		}
		key, available, readErr := editor.readEditorKey()
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				editor.Prompt = originalPrompt
				restoreHistorySearchDraft(editor.Core, original, originalCursor)
				return false, readErr
			}
			break
		}
		if !available {
			continue
		}
		switch key {
		case 0x12, 0x13: // Ctrl-R searches older; Ctrl-S searches newer.
			direction = -1
			if key == 0x13 {
				direction = 1
			}
			if len(query) == 0 || noMatch {
				continue
			}
			found := editor.Core.HistorySearch(string(query), match+direction, direction)
			if found >= 0 {
				match = found
			}
		case 0x08, 0x7f: // Backspace edits the query by one UTF-8 code point.
			if len(query) > 0 {
				_, size := utf8.DecodeLastRune(query)
				query = query[:len(query)-size]
			}
			match, noMatch = recomputeHistoryMatch(editor.Core, string(query), direction, match)
		case 0x07, 0x03: // Ctrl-G and Ctrl-C cancel search.
			goto done
		case '\r':
			accepted = true
			submit = true
			goto done
		case '\n':
			accepted = true
			goto done
		case 0x1b:
			action, escapeErr := editor.decodeEscapeAction()
			if escapeErr != nil {
				editor.Prompt = originalPrompt
				restoreHistorySearchDraft(editor.Core, original, originalCursor)
				return false, escapeErr
			}
			if action == ActionPasteBegin {
				body, pasteErr := editor.readPasteBody()
				if pasteErr != nil {
					editor.Prompt = originalPrompt
					restoreHistorySearchDraft(editor.Core, original, originalCursor)
					return false, pasteErr
				}
				for _, value := range []byte(body) {
					if value >= 0x20 && value != 0x7f {
						query = append(query, value)
					}
				}
				match, noMatch = recomputeHistoryMatch(editor.Core, string(query), direction, match)
				continue
			}
			accepted = true
			goto done
		default:
			if key >= 0x20 {
				sequence, sequenceErr := editor.readUTF8Sequence(key)
				if sequenceErr != nil {
					editor.Prompt = originalPrompt
					restoreHistorySearchDraft(editor.Core, original, originalCursor)
					return false, sequenceErr
				}
				query = append(query, sequence...)
				match, noMatch = recomputeHistoryMatch(editor.Core, string(query), direction, match)
			}
		}
	}

done:
	editor.Prompt = originalPrompt
	if accepted && !noMatch && match >= 0 {
		showHistorySearchMatch(editor.Core, original, originalCursor, string(query), match, false)
		editor.Core.AcceptHistory(match, original, originalHistoryPosition == editor.Core.HistoryLen())
	} else {
		restoreHistorySearchDraft(editor.Core, original, originalCursor)
		submit = false
	}
	return submit, nil
}

func showHistorySearchMatch(core *Core, original string, originalCursor int, query string, match int, noMatch bool) {
	if noMatch {
		core.SetBuffer("")
		return
	}
	if match < 0 {
		restoreHistorySearchDraft(core, original, originalCursor)
		return
	}
	entries := core.HistoryEntries()
	if match >= len(entries) {
		restoreHistorySearchDraft(core, original, originalCursor)
		return
	}
	entry := entries[match]
	core.SetBuffer(entry)
	if index := strings.Index(entry, query); index >= 0 {
		core.SetCursor(index)
	}
}

func restoreHistorySearchDraft(core *Core, original string, cursor int) {
	core.SetBuffer(original)
	core.SetCursor(cursor)
}

func recomputeHistoryMatch(core *Core, query string, direction, previous int) (int, bool) {
	if query == "" {
		return -1, false
	}
	start := previous
	if start < 0 {
		if direction < 0 {
			start = core.HistoryLen() - 1
		} else {
			start = 0
		}
	}
	found := core.HistorySearch(query, start, direction)
	if found >= 0 {
		return found, false
	}
	return previous, true
}

func buildHistorySearchPrompt(query string, direction int, noMatch bool, columns int) string {
	return buildHistorySearchPromptForLocale(query, direction, noMatch, columns, terminalocale.HaveUTF8())
}

func buildHistorySearchPromptForLocale(query string, direction int, noMatch bool, columns int, haveUTF8 bool) string {
	label := "reverse-search"
	if direction > 0 {
		label = "forward-search"
	}
	query = safeDisplayText(query)
	separator := ""
	if query != "" {
		separator = " · "
		if !haveUTF8 {
			separator = " : "
		}
	}
	arrow := " → "
	if !haveUTF8 {
		arrow = " > "
	}
	suffix := ""
	if noMatch {
		suffix = " (no match)"
	}
	fixed := label + separator + arrow + suffix
	available := columns - 1 - displayTextWidth(fixed)
	if available >= 0 {
		return label + separator + clipDisplayTail(query, available, haveUTF8) + arrow + suffix
	}
	plain := label + separator + query + arrow + suffix
	budget := columns - 1
	if budget < 1 {
		budget = 1
	}
	return clipDisplayTail(plain, budget, haveUTF8)
}

func safeDisplayText(text string) string {
	var safe strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		invalid := r == utf8.RuneError && size == 1 && text[0] >= utf8.RuneSelf
		text = text[size:]
		if unsafeCodepoint(r) || invalid {
			safe.WriteByte('?')
		} else {
			safe.WriteRune(r)
		}
	}
	return safe.String()
}

func displayTextWidth(text string) int {
	width := 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		width += runeDisplayWidth(r)
	}
	return width
}

func clipDisplayTail(text string, width int, haveUTF8 bool) string {
	if width <= 0 {
		return ""
	}
	if displayTextWidth(text) <= width {
		return text
	}
	marker := "…"
	if !haveUTF8 {
		marker = "<"
	}
	if width == 1 {
		return marker
	}
	budget := width - 1
	runes := []rune(text)
	start := len(runes)
	used := 0
	for start > 0 {
		nextWidth := runeDisplayWidth(runes[start-1])
		if used+nextWidth > budget {
			break
		}
		used += nextWidth
		start--
	}
	return marker + string(runes[start:])
}

func (editor *Editor) repaint() error {
	return editor.paint()
}

func runeDisplayWidth(r rune) int {
	if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
		return 1
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe19 ||
		r >= 0xfe30 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 ||
		r >= 0x1f300 && r <= 0x1faff || r >= 0x20000 && r <= 0x3fffd) {
		return 2
	}
	return 1
}

func utf8SequenceLength(value byte) int {
	switch {
	case value&0xe0 == 0xc0:
		return 2
	case value&0xf0 == 0xe0:
		return 3
	case value&0xf8 == 0xf0:
		return 4
	default:
		return 1
	}
}
