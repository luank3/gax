package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminaledit "github.com/luank3/gax/internal/terminal/editbuffer"
	"github.com/luank3/gax/internal/terminal/filemention"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalpaste "github.com/luank3/gax/internal/terminal/paste"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

func runREPL(ctx context.Context, stdin io.Reader, inputTTY, persistPromptHistory bool, stdout, stderr io.Writer,
	store *config.Store, tools *coretools.Registry, state *agent.Session, run func(string) replOutput, pendingInput *string,
	argumentChoices slash.ArgumentChoices, handleCommand func(slash.Parsed) bool) int {
	if terminalinput.TTYPair(stdin, stdout, inputTTY) {
		return runTTYREPL(ctx, stdin.(*os.File), persistPromptHistory, stdout, stderr, store, tools, state, run,
			pendingInput, argumentChoices, handleCommand)
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	type scanResult struct {
		text string
		err  error
		ok   bool
	}
	lines := make(chan scanResult)
	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanResult{text: scanner.Text(), ok: true}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- scanResult{err: scanner.Err()}:
		case <-ctx.Done():
		}
	}()
	for {
		if display, ok := stdout.(*lineOutputWriter); ok {
			display.BlockSeparator()
		}
		if inputTTY {
			fmt.Fprint(stdout, "> ")
		}
		select {
		case <-ctx.Done():
			return 130
		case line := <-lines:
			if !line.ok {
				if line.err != nil {
					fmt.Fprintf(stderr, "gax: read prompt: %v\n", line.err)
					return 1
				}
				fmt.Fprintln(stdout)
				return 0
			}
			text := line.text
			if strings.TrimSpace(text) == "" && (state == nil || state.ResumeReason == agent.ResumeReasonNone) {
				continue
			}
			if code, done := processREPLLine(text, stdout, stderr, store, run, handleCommand); done {
				return code
			}
		}
	}
}

func ttyPrompt(haveUTF8 bool) string {
	if !haveUTF8 {
		return "\x1b[1m>\x1b[22m "
	}
	return terminaltheme.Open(terminaltheme.Accent) + "\x1b[1m❯\x1b[22m" + terminaltheme.Close(terminaltheme.Accent) + " "
}

const (
	ansiCursorHide = "\x1b[?25l"
	ansiCursorShow = "\x1b[?25h"
)

func hideCursor(stdout io.Writer) { _, _ = io.WriteString(stdout, ansiCursorHide) }

func showCursor(stdout io.Writer) { _, _ = io.WriteString(stdout, ansiCursorShow) }

func runTTYREPL(ctx context.Context, stdin *os.File, persistPromptHistory bool, stdout, stderr io.Writer,
	store *config.Store, tools *coretools.Registry, state *agent.Session, run func(string) replOutput, pendingInput *string,
	argumentChoices slash.ArgumentChoices, handleCommand func(slash.Parsed) bool) int {
	core := terminalinput.NewCore()
	cwd, _ := os.Getwd()
	historyPath, _ := session.PromptHistoryPath(cwd)
	history := terminalinput.OpenHistory(core, historyPath, persistPromptHistory)
	editorOptions := terminalinput.EditorOptions{
		ModalKeys: map[byte]func(){
			0x0f: historyModalKey(state, func() bool { return store.Bool("show_reasoning") }, tools, store, stdout, stderr), // Ctrl-O
			0x14: transcriptModalKey(state, stdout, stderr),                                                                 // Ctrl-T
		},
		Completers: []terminalinput.Completer{slashCompleter(argumentChoices), {
			Match: filemention.Match,
			Complete: func(token string) (string, bool) {
				if !filemention.Available() {
					writeWarningDiagnostic(stderr, "@file completion needs fzf installed")
					return "", false
				}
				query := strings.TrimPrefix(token, "@")
				path, ok, err := filemention.Pick(query)
				if err != nil {
					writeWarningDiagnostic(stderr, "%v", err)
					return "", false
				}
				return path, ok
			},
			Modal: true,
		}},
		EditBuffer: func(buffer string) (string, bool) {
			text, ok, err := terminaledit.EditBuffer(buffer, stdin, stdout, stderr)
			if err != nil {
				writeWarningDiagnostic(stderr, "%v", err)
				return "", false
			}
			return text, ok
		},
		Hint: func(buffer string) (string, bool) {
			if buffer == "" && state != nil {
				if hint := state.ResumeReason.Placeholder(); hint != "" {
					return hint, true
				}
			}
			return slash.Hint(buffer)
		},
		DisplayColumns: func(terminalColumns int) int { return editorDisplayColumns(store, terminalColumns) },
		PasteHook:      func() (string, bool) { return terminalpaste.CaptureClipboard(ctx) },
		PasteFilter:    terminalpaste.FileURIsToPaths,
	}
	// A turn runs with the cursor hidden: the editor owns the cursor for
	// the prompt, and everything between prompts (streamed answers, tool output, the animated
	// indicator) paints with it off. The deferred show keeps an abandoned cursor from reaching
	// the parent shell.
	defer showCursor(stdout)
	for {
		if ctx.Err() != nil {
			return 130
		}
		editorOptions.InitialText = ""
		editorOptions.EmptySubmit = state != nil && state.ResumeReason != agent.ResumeReasonNone
		if pendingInput != nil {
			editorOptions.InitialText = *pendingInput
			*pendingInput = ""
		}
		prompt := ttyPrompt(terminalocale.HaveUTF8())
		showCursor(stdout)
		text, submitted, err := terminalinput.ReadTTYLine(ctx, stdin, stdout, core, history, prompt, editorOptions)
		hideCursor(stdout)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return 130
			}
			fmt.Fprintf(stderr, "gax: read prompt: %v\n", err)
			return 1
		}
		if !submitted {
			if ctx.Err() != nil {
				return 130
			}
			return 0
		}
		if strings.TrimSpace(text) == "" && (state == nil || state.ResumeReason == agent.ResumeReasonNone) {
			continue
		}
		if text != "" && slash.Parse(text).Outcome == slash.NotCommand {
			history.Add(core, text)
		}
		isCommand := slash.Parse(text).Outcome != slash.NotCommand
		if isCommand {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return 1
			}
		}
		code, done := processREPLLine(text, stdout, stderr, store, run, handleCommand)
		if isCommand && !done {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return 1
			}
		}
		if done {
			return code
		}
	}
}

func editorDisplayColumns(store *config.Store, terminalColumns int) int {
	mode := "auto"
	configured := 0
	if store != nil {
		if value := store.String("display_width"); value != nil {
			mode = *value
		}
		configured = store.Int("display_width")
	}
	return terminalwidth.ContentColumns(terminalColumns, mode, configured)
}

func markdownDisplayColumns(store *config.Store, terminalColumns int) int {
	columns := editorDisplayColumns(store, terminalColumns)
	if edge := terminalColumns - 1; edge > 0 && columns > edge {
		columns = edge
	}
	return columns
}

// processREPLLine returns done=true when the REPL should exit. Commands and empty lines leave the
// loop running; provider failures and writer failures terminate it with their corresponding status.
func processREPLLine(text string, stdout, stderr io.Writer, store *config.Store,
	run func(string) replOutput, handleCommand func(slash.Parsed) bool) (int, bool) {
	parsed := slash.Parse(text)
	if text != "" {
		if display, ok := stdout.(*lineOutputWriter); ok {
			display.SyncExternalLine()
			display.BlockSeparator()
		}
	}
	if parsed.Outcome != slash.NotCommand {
		if handleCommand != nil {
			handleCommand(parsed)
		}
		return 0, false
	}
	output := run(text)
	result, answer := output.result, output.answer
	notify := func() {
		if result.Turns > 0 {
			emitAttentionNotification(stdout, store)
		}
	}
	writeOutput := func() bool {
		if err := writeAssistantAnswer(stdout, answer, store); err != nil {
			fmt.Fprintf(stderr, "gax: write output: %v\n", err)
			return false
		}
		if output.afterAnswer != nil {
			if err := output.afterAnswer(stdout); err != nil {
				fmt.Fprintf(stderr, "gax: write output: %v\n", err)
				return false
			}
		}
		return true
	}
	switch result.Outcome {
	case agent.LoopComplete:
	case agent.LoopPaused:
		if !writeOutput() {
			return 1, true
		}
		return 0, false
	case agent.LoopMaxTurns:
		if !writeOutput() {
			return 1, true
		}
		notify()
		return 0, false
	case agent.LoopProviderError:
		if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "gax: %s\n", result.ErrorMessage)
		}
		notify()
		return 0, false
	case agent.LoopInterrupted:
		if !writeOutput() {
			return 1, true
		}
		return 0, false
	default:
		if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "gax: %s\n", result.ErrorMessage)
		}
		notify()
		return 1, true
	}
	if !writeOutput() {
		return 1, true
	}
	notify()
	return 0, false
}
