package input_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/luank3/gax/internal/slash"
	"github.com/luank3/gax/internal/terminal/input"
	"github.com/luank3/gax/internal/terminal/paste"
)

type scriptedBytes struct {
	values []byte
	pos    int
}

func (source *scriptedBytes) ReadByteTimeout(_ time.Duration) (byte, bool, error) {
	if source.pos == len(source.values) {
		return 0, false, io.EOF
	}
	value := source.values[source.pos]
	source.pos++
	return value, true, nil
}

func TestEditorReadsAndEditsLine(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte("hello\x1b[D!\x01>\r")}
	core := input.NewCore()
	editor := input.Editor{Core: core, Prompt: "> ", Reader: source, Writer: &screen}

	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted {
		t.Fatalf("ReadLine() = %q, %t, %v; want submitted line", line, submitted, err)
	}
	if line != ">hell!o" {
		t.Fatalf("ReadLine() line = %q, want %q", line, ">hell!o")
	}
	if !strings.Contains(screen.String(), "\x1b[J") {
		t.Fatalf("editor did not repaint the edit area: %q", screen.String())
	}
	if !strings.Contains(screen.String(), "▌ >hell!o") {
		t.Fatalf("submitted prompt was not committed with its user-message stripe: %q", screen.String())
	}
}

func TestEditorStartsWithPreseededText(t *testing.T) {
	editor := input.Editor{
		Core: input.NewCore(), InitialText: "/preset-save ",
		Reader: &scriptedBytes{values: []byte("review violet\r")}, Writer: io.Discard,
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "/preset-save review violet" {
		t.Fatalf("ReadLine() = %q, %t, %v; want the preseed plus typed suffix", line, submitted, err)
	}
}

func TestEditorModalKeyRunsCallbackWithoutChangingDraft(t *testing.T) {
	calls := 0
	editor := input.Editor{
		Core: input.NewCore(), InitialText: "before ",
		Reader: &scriptedBytes{values: []byte("\x14after\r")}, Writer: io.Discard,
		ModalKeys: map[byte]func(){0x14: func() { calls++ }},
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "before after" {
		t.Fatalf("ReadLine() = %q, %t, %v; want draft preserved across modal key", line, submitted, err)
	}
	if calls != 1 {
		t.Fatalf("modal key callback count = %d, want one", calls)
	}
}

func TestEditorAllowsEmptySubmitOnlyWhenEnabled(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		want    bool
	}{
		{name: "default", want: false},
		{name: "resume", enabled: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := input.Editor{Core: input.NewCore(), EmptySubmit: test.enabled,
				Reader: &scriptedBytes{values: []byte("\r")}, Writer: io.Discard}
			_, submitted, err := editor.ReadLine()
			if err != nil || submitted != test.want {
				t.Fatalf("ReadLine() submitted=%t err=%v, want submitted=%t", submitted, err, test.want)
			}
		})
	}
}

func TestEditorCompletesSlashNameAndShowsCandidatesOnRepeatedTab(t *testing.T) {
	completer := input.Completer{
		Match:    slash.MatchName,
		Complete: slash.CompleteName,
		Candidates: func(token string) (string, bool) {
			if token == "" {
				return "  see /help", true
			}
			return slash.NameCandidates(token)
		},
	}
	var screen bytes.Buffer
	editor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("/c\t\t\r")}, Writer: &screen,
		Completers: []input.Completer{completer},
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "/c" {
		t.Fatalf("ReadLine() = %q, %t, %v; want unchanged ambiguous command", line, submitted, err)
	}
	if !strings.Contains(screen.String(), "/clear /config") {
		t.Fatalf("repeated Tab did not show slash candidates: %q", screen.String())
	}

	var uniqueScreen bytes.Buffer
	uniqueEditor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("/mo\t\r")}, Writer: &uniqueScreen,
		Completers: []input.Completer{completer},
	}
	line, submitted, err = uniqueEditor.ReadLine()
	if err != nil || !submitted || line != "/model " {
		t.Fatalf("unique ReadLine() = %q, %t, %v; want completed command", line, submitted, err)
	}
}

func TestEditorRendersSlashHintAndClearsGhostBeforeSubmit(t *testing.T) {
	var screen bytes.Buffer
	editor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("/new\r")}, Writer: &screen,
		Hint: slash.Hint,
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "/new" {
		t.Fatalf("ReadLine() = %q, %t, %v; want command line", line, submitted, err)
	}
	if !strings.Contains(screen.String(), " [preset]") {
		t.Fatalf("slash hint was not rendered: %q", screen.String())
	}
	if !strings.Contains(screen.String(), "\x1b[K\r\n") {
		t.Fatalf("ghost text was not erased before leaving the editor: %q", screen.String())
	}
}

func TestEditorCtrlVPastesAtCursor(t *testing.T) {
	called := false
	editor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("ab\x02\x16\r")}, Writer: io.Discard,
		PasteHook: func() (string, bool) {
			called = true
			return "X", true
		},
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "aXb" || !called {
		t.Fatalf("ReadLine() = %q, %t, %v, paste called=%t; want pasted text at cursor", line, submitted, err, called)
	}
}

func TestEditorBracketedPasteFiltersFileURIs(t *testing.T) {
	editor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("prefix\x1b[200~file:///tmp/pic.PNG\x1b[201~\r")}, Writer: io.Discard,
		PasteFilter: paste.FileURIsToPaths,
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "prefix[pasted image: /tmp/pic.PNG] " {
		t.Fatalf("ReadLine() = %q, %t, %v; want converted pasted image URI", line, submitted, err)
	}
}

func TestEditorEmptyBracketedPasteCallsPasteHook(t *testing.T) {
	called := false
	editor := input.Editor{
		Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("prefix\x1b[200~\x1b[201~\r")}, Writer: io.Discard,
		PasteHook: func() (string, bool) {
			called = true
			return "clipboard", true
		},
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "prefixclipboard" || !called {
		t.Fatalf("ReadLine() = %q, %t, %v, paste called=%t; want clipboard fallback", line, submitted, err, called)
	}
}

func TestEditorUsesHistoryAndRestoresDraft(t *testing.T) {
	var screen bytes.Buffer
	core := input.NewCore()
	core.AddHistory("first")
	core.AddHistory("second")
	source := &scriptedBytes{values: []byte("draft\x10\x10\x0e\x0e\r")}
	editor := input.Editor{Core: core, Reader: source, Writer: &screen}

	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "draft" {
		t.Fatalf("ReadLine() = %q, %t, %v; want restored draft", line, submitted, err)
	}
}

func TestEditorBracketedPasteNormalizesNewlinesAndNUL(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte("\x1b[200~one\r\ntwo\x00\x1b[201~\r")}
	editor := input.Editor{Core: input.NewCore(), Reader: source, Writer: &screen}

	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "one\ntwo " {
		t.Fatalf("ReadLine() = %q, %t, %v; want normalized paste", line, submitted, err)
	}
}

func TestEditorSanitizesPastedTerminalControlsForDisplay(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte("\x1b[200~\x1b[31mtext\x1b[201~\r")}
	editor := input.Editor{Core: input.NewCore(), Reader: source, Writer: &screen}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "\x1b[31mtext" {
		t.Fatalf("ReadLine() = %q, %t, %v", line, submitted, err)
	}
	if strings.Contains(screen.String(), "\x1b[31m") {
		t.Fatalf("pasted control sequence was emitted to terminal: %q", screen.String())
	}
}

func TestEditorCursorBeforeNewlineAtRightMargin(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte("abc\n\x1b[D\r")}
	editor := input.Editor{Core: input.NewCore(), Prompt: "> ", Reader: source, Writer: &screen, Columns: 5}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "abc\n" {
		t.Fatalf("ReadLine() = %q, %t, %v", line, submitted, err)
	}
	if !strings.Contains(screen.String(), "\x1b[4C") {
		t.Fatalf("cursor before newline was not restored to the right margin: %q", screen.String())
	}
}

func TestEditorPainterClipsTallMultilineBufferToViewport(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte("a\nb\nc\nd\r")}
	editor := input.Editor{
		Core: input.NewCore(), Prompt: "> ", Reader: source, Writer: &screen,
		Columns: 20, Rows: 5,
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "a\nb\nc\nd" {
		t.Fatalf("ReadLine() = %q, %t, %v", line, submitted, err)
	}
	output := screen.String()
	lastFrame := strings.LastIndex(output, "\x1b[?2026h")
	if lastFrame < 0 {
		t.Fatalf("editor did not use synchronized repaint: %q", output)
	}
	frame := output[lastFrame:]
	if !strings.Contains(frame, "… +3 lines") || !strings.Contains(frame, "  d") {
		t.Fatalf("clipped frame lacks the top indicator or visible tail: %q", frame)
	}
}

func TestEditorReverseHistorySearchSubmitsNewestMatch(t *testing.T) {
	core := input.NewCore()
	core.AddHistory("git status")
	core.AddHistory("git log")
	core.AddHistory("make test")
	editor := input.Editor{Core: core, Reader: &scriptedBytes{values: []byte("\x12git\r")}, Writer: io.Discard}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "git log" {
		t.Fatalf("ReadLine() = %q, %t, %v; want newest matching history entry", line, submitted, err)
	}
}

func TestEditorReverseHistorySearchRepeatsOlderAndPreservesDraft(t *testing.T) {
	core := input.NewCore()
	core.AddHistory("foo one")
	core.AddHistory("foo two")
	core.AddHistory("other")
	editor := input.Editor{Core: core, Reader: &scriptedBytes{values: []byte("draft\x12foo\x12\r")}, Writer: io.Discard}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "foo one" {
		t.Fatalf("ReadLine() = %q, %t, %v; want older matching entry", line, submitted, err)
	}
	core.HistoryNext()
	if core.Text() != "foo two" {
		t.Fatalf("history after search = %q, want next newer match", core.Text())
	}
	core.HistoryNext()
	if core.Text() != "other" {
		t.Fatalf("history after second down = %q, want newest entry", core.Text())
	}
	core.HistoryNext()
	if core.Text() != "draft" {
		t.Fatalf("history did not restore the original draft: %q", core.Text())
	}
}

func TestEditorHistorySearchRetainsPreviousMatchWhileQueryIsUnmatched(t *testing.T) {
	core := input.NewCore()
	core.AddHistory("foo older")
	core.AddHistory("foo newer")
	keys := "\x12foo\x12x\x08\x1b\r"
	editor := input.Editor{Core: core, Reader: &scriptedBytes{values: []byte(keys)}, Writer: io.Discard}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "foo older" {
		t.Fatalf("ReadLine() = %q, %t, %v; want retained older match", line, submitted, err)
	}
}

func TestEditorHistorySearchCancelAndNoMatchRestoreOriginalDraft(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
	}{
		{name: "cancel", keys: "draft\x12x\x07\r"},
		{name: "no match", keys: "draft\x12x\r\r"},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := input.Editor{Core: input.NewCore(), Reader: &scriptedBytes{values: []byte(test.keys)}, Writer: io.Discard}
			line, submitted, err := editor.ReadLine()
			if err != nil || !submitted || line != "draft" {
				t.Fatalf("ReadLine() = %q, %t, %v; want original draft", line, submitted, err)
			}
		})
	}
}

func TestEditorEOFAndDoubleCtrlC(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
	}{
		{name: "ctrl-d", keys: "\x04"},
		{name: "double ctrl-c", keys: "\x03\x03"},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := input.Editor{Core: input.NewCore(), Reader: &scriptedBytes{values: []byte(test.keys)}, Writer: io.Discard}
			line, submitted, err := editor.ReadLine()
			if err != nil || submitted || line != "" {
				t.Fatalf("ReadLine() = %q, %t, %v; want EOF without submission", line, submitted, err)
			}
		})
	}
}

func TestEditorPropagatesReaderError(t *testing.T) {
	wantErr := errors.New("read failed")
	editor := input.Editor{
		Core: input.NewCore(),
		Reader: input.ByteReaderFunc(func(time.Duration) (byte, bool, error) {
			return 0, false, wantErr
		}),
		Writer: io.Discard,
	}
	if _, _, err := editor.ReadLine(); !errors.Is(err, wantErr) {
		t.Fatalf("ReadLine() error = %v, want %v", err, wantErr)
	}
}

func TestEditorMalformedUTF8DoesNotConsumeFollowingControlKey(t *testing.T) {
	var screen bytes.Buffer
	source := &scriptedBytes{values: []byte{0xc3, 0x04}}
	editor := input.Editor{Core: input.NewCore(), Reader: source, Writer: &screen}

	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "\uFFFD" {
		t.Fatalf("ReadLine() = %q, %t, %v; Ctrl-D after malformed UTF-8 should leave only the sanitized leader", line, submitted, err)
	}
}

func TestEditorEOFSubmitsNonemptyDraft(t *testing.T) {
	var screen bytes.Buffer
	editor := input.Editor{Core: input.NewCore(), Reader: &scriptedBytes{values: []byte("partial")}, Writer: &screen}

	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "partial" {
		t.Fatalf("ReadLine() = %q, %t, %v; want buffered line at EOF", line, submitted, err)
	}
}
