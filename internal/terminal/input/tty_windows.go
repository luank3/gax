//go:build windows

package input

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/luank3/gax/internal/system/console"
)

const (
	bracketedPasteEnable  = "\x1b[?2004h"
	bracketedPasteDisable = "\x1b[?2004l"
)

// TTYPair reports whether both streams are native Windows consoles.
func TTYPair(stdin io.Reader, stdout io.Writer, inputTTY bool) bool {
	in, ok := stdin.(*os.File)
	if !ok || !inputTTY {
		return false
	}
	out, ok := stdout.(*os.File)
	return ok && console.IsTerminal(in) && console.IsTerminal(out)
}

// ReadTTYLine scopes Windows console raw mode to one prompt.
func ReadTTYLine(ctx context.Context, stdin *os.File, stdout io.Writer, core *Core, history *HistoryFile, prompt string, options ...EditorOptions) (string, bool, error) {
	if stdin == nil || stdout == nil {
		return "", false, errors.New("input terminal is unavailable")
	}
	out, ok := stdout.(*os.File)
	if !ok {
		return "", false, errors.New("output terminal is unavailable")
	}
	mode, err := console.Raw(stdin, out, false)
	if err != nil {
		return "", false, err
	}
	defer func() { mode.Restore() }()
	if _, err := io.WriteString(stdout, bracketedPasteEnable); err != nil {
		_, _ = io.WriteString(stdout, bracketedPasteDisable)
		return "", false, err
	}
	defer func() { _, _ = io.WriteString(stdout, bracketedPasteDisable) }()
	terminalColumns, rows := queryTerminalSize(stdout, 100, 0)
	columns := editorColumnsForOptions(terminalColumns, options)
	initialText := ""
	emptySubmit := false
	for _, option := range options {
		if option.InitialText != "" {
			initialText = option.InitialText
		}
		emptySubmit = emptySubmit || option.EmptySubmit
	}
	editor := Editor{Core: core, History: history, InitialText: initialText, EmptySubmit: emptySubmit, Prompt: prompt,
		Reader: &console.Source{File: stdin}, Writer: stdout, Context: ctx, Columns: columns, Rows: rows}
	for _, option := range options {
		for key, callback := range option.ModalKeys {
			if editor.ModalKeys == nil {
				editor.ModalKeys = make(map[byte]func())
			}
			editor.ModalKeys[key] = callback
		}
	}
	editor.terminalSize = func() (int, int) {
		terminalColumns, rows := queryTerminalSize(stdout, 100, 0)
		return editorColumnsForOptions(terminalColumns, options), rows
	}
	for _, option := range options {
		editor.Completers = append(editor.Completers, option.Completers...)
		if option.EditBuffer != nil {
			editor.EditBuffer = option.EditBuffer
		}
		if option.Hint != nil {
			editor.Hint = option.Hint
		}
		if option.PasteHook != nil {
			editor.PasteHook = option.PasteHook
		}
		if option.PasteFilter != nil {
			editor.PasteFilter = option.PasteFilter
		}
	}
	editor.modalRun = func(run func() (string, bool)) (string, bool, error) {
		if err := editor.eraseEditArea(); err != nil {
			return "", false, err
		}
		mode.Restore()
		text, ok := run()
		mode, err = console.Raw(stdin, out, false)
		if err != nil {
			return "", false, err
		}
		if editor.terminalSize != nil {
			columns, rows := editor.terminalSize()
			editor.resize(columns, rows)
		}
		return text, ok, nil
	}
	return editor.ReadLine()
}

func queryTerminalSize(writer io.Writer, fallbackColumns, fallbackRows int) (int, int) {
	if out, ok := writer.(*os.File); ok {
		if columns, rows, ok := console.Size(out); ok {
			return columns, rows
		}
	}
	return fallbackColumns, fallbackRows
}

func editorColumnsForOptions(terminalColumns int, options []EditorOptions) int {
	columns := terminalColumns
	for _, option := range options {
		if option.DisplayColumns != nil {
			columns = option.DisplayColumns(terminalColumns)
			break
		}
	}
	if terminalColumns > 0 && columns > terminalColumns {
		columns = terminalColumns
	}
	if columns <= 0 {
		return 1
	}
	return columns
}
