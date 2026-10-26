package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	terminalocale "github.com/luank3/gax/internal/system/locale"
	systemshell "github.com/luank3/gax/internal/system/shell"
	"github.com/luank3/gax/internal/system/spawn"
	terminalvt "github.com/luank3/gax/internal/terminal/vtresolve"
	coretools "github.com/luank3/gax/internal/tools"
	"github.com/luank3/gax/internal/transcript"
)

func transcriptModalKey(state *agent.Session, stdout, stderr io.Writer) func() {
	return func() { showTranscript(state, stdout, stderr) }
}

func showTranscript(state *agent.Session, stdout, stderr io.Writer) {
	command, err := resolvePager()
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		_, _ = io.WriteString(stdout, "\n")
		return
	}

	renderWithPager(command, stdout, stderr, func(writer io.Writer) {
		if state != nil {
			_ = transcript.Render(writer, state.Context(), transcript.ANSI)
		}
	})
}

func historyModalKey(state *agent.Session, storeReasoning func() bool, registry *coretools.Registry,
	store *config.Store, stdout, stderr io.Writer) func() {
	return func() {
		showHistoryWithTools(state, storeReasoning != nil && storeReasoning(), registry, store, stdout, stderr)
	}
}

func showHistory(state *agent.Session, showReasoning bool, stdout, stderr io.Writer) {
	showHistoryWithTools(state, showReasoning, nil, nil, stdout, stderr)
}

func showHistoryWithTools(state *agent.Session, showReasoning bool, registry *coretools.Registry,
	store *config.Store, stdout, stderr io.Writer) {
	command, err := resolvePager()
	if err != nil {
		fmt.Fprintf(stderr, "gax: %v\n", err)
		_, _ = io.WriteString(stdout, "\n")
		return
	}
	var raw bytes.Buffer
	if state == nil || len(state.Items) == 0 {
		_, _ = io.WriteString(&raw, "\x1b[2m"+historyHeading(0, terminalocale.HaveUTF8())+"\x1b[22m\n\n(nothing in this conversation yet)\n")
	} else {
		prompts := 0
		items := state.Items
		for _, item := range items {
			if conversation.IsTypedPrompt(item) {
				prompts++
			}
		}
		_, _ = io.WriteString(&raw, "\x1b[2m"+historyHeading(prompts, terminalocale.HaveUTF8())+"\x1b[22m\n\n")
		if err := renderHistoryItems(&raw, items, registry, appDisplayWidth(store), showReasoning, historyFull); err != nil {
			fmt.Fprintf(stderr, "gax: render history: %v\n", err)
		}
	}
	resolved := terminalvt.Resolve(raw.Bytes())
	renderWithPager(command, stdout, stderr, func(writer io.Writer) {
		_, _ = writer.Write(resolved)
	})
}

func renderWithPager(command string, stdout, stderr io.Writer, render func(io.Writer)) {
	reader, writer := io.Pipe()
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = systemshell.Default()
	}
	child := exec.Command(shell, "-c", command)
	child.Stdin = reader
	child.Stdout = stdout
	child.Stderr = stderr
	child.Env = terminalocale.ForceUTF8ChildEnvironment(os.Environ())
	releaseInterrupts := spawn.SuppressParentInterrupts()
	defer releaseInterrupts()
	if err := child.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		fmt.Fprintf(stderr, "gax: couldn't start pager: %v\n", err)
		_, _ = io.WriteString(stdout, "\n")
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		render(writer)
	}()
	_ = child.Wait()
	_ = reader.CloseWithError(io.EOF)
	<-done
}

func resolvePager() (string, error) {
	configured := os.Getenv("PAGER")
	if configured != "" {
		head, end := shellHead(configured)
		if head == "" {
			return "", fmt.Errorf("$PAGER (%s) not found — fix it, or unset it to use a fallback", configured)
		}
		if systemshell.FindCommand(head) == "" {
			return "", fmt.Errorf("$PAGER (%s) not found — fix it, or unset it to use a fallback", configured)
		}
		return addLessColorFlag(configured, head, end), nil
	}
	for _, candidate := range []string{"less", "more"} {
		if systemshell.FindCommand(candidate) != "" {
			return addLessColorFlag(candidate, candidate, len(candidate)), nil
		}
	}
	return "", errors.New("no pager found — install less or set $PAGER")
}

// shellHead reads the first shell word and returns its source end offset, honoring ordinary
// single/double quotes and backslash escapes so paths containing spaces remain usable.
func shellHead(command string) (string, int) {
	index := 0
	for index < len(command) && (command[index] == ' ' || command[index] == '\t') {
		index++
	}
	start := index
	var word strings.Builder
	quote := byte(0)
	escaped := false
	for index < len(command) {
		current := command[index]
		if escaped {
			word.WriteByte(current)
			escaped = false
			index++
			continue
		}
		if quote != '\'' && current == '\\' {
			escaped = true
			index++
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			} else {
				word.WriteByte(current)
			}
			index++
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			index++
			continue
		}
		if current == ' ' || current == '\t' || current == '\n' || current == '|' || current == ';' || current == '&' {
			break
		}
		word.WriteByte(current)
		index++
	}
	if escaped {
		word.WriteByte('\\')
	}
	if quote != 0 {
		return "", 0
	}
	if index == start {
		return "", 0
	}
	return word.String(), index
}

func addLessColorFlag(command, head string, end int) string {
	base := head
	if slash := strings.LastIndexAny(base, "/\\"); slash >= 0 {
		base = base[slash+1:]
	}
	base = strings.ToLower(base)
	if base != "less" && base != "less.exe" {
		return command
	}
	return command[:end] + " -R" + command[end:]
}

func historyHeading(prompts int, haveUTF8 bool) string {
	rule := "--"
	if haveUTF8 {
		rule = "──"
	}
	var title strings.Builder
	title.WriteString(rule + " conversation")
	if prompts > 0 {
		fmt.Fprintf(&title, " · %d prompt", prompts)
		if prompts != 1 {
			title.WriteByte('s')
		}
	}
	title.WriteString(" " + rule)
	return title.String()
}
