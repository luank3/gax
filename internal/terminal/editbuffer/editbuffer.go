// Package editbuffer implements the cooked-mode $EDITOR handoff for the terminal prompt.
package editbuffer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	terminalocale "github.com/luank3/gax/internal/system/locale"
	systemshell "github.com/luank3/gax/internal/system/shell"
	"github.com/luank3/gax/internal/system/spawn"
)

var fallbackEditors = []string{"editor", "nano", "vim", "vi"}

// EditBuffer opens the current text in the configured editor, returning its saved contents after
// a successful exit. A cancelled or failed editor leaves the caller's buffer unchanged.
func EditBuffer(text string, stdin io.Reader, stdout, stderr io.Writer) (string, bool, error) {
	command, err := resolveEditor()
	if err != nil {
		return "", false, err
	}
	temporary, err := os.CreateTemp("", "gax-edit-*")
	if err != nil {
		return "", false, err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if _, err := io.WriteString(temporary, text); err != nil {
		_ = temporary.Close()
		return "", false, err
	}
	if err := temporary.Close(); err != nil {
		return "", false, err
	}
	commandLine := command + " " + shellQuote(path)
	process := systemshell.Command(systemshell.Default(), commandLine)
	process.Stdin = stdin
	process.Stdout = stdout
	process.Stderr = stderr
	process.Env = terminalocale.ForceUTF8ChildEnvironment(os.Environ())
	err = func() error {
		releaseInterrupts := spawn.SuppressParentInterrupts()
		defer releaseInterrupts()
		return process.Run()
	}()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return "", false, nil
		}
		return "", false, err
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	for index, value := range updated {
		if value == 0 {
			updated[index] = ' '
		}
	}
	if len(updated) > 0 && updated[len(updated)-1] == '\n' {
		updated = updated[:len(updated)-1]
	}
	return string(updated), true, nil
}

func resolveEditor() (string, error) {
	configured, source := os.Getenv("VISUAL"), "$VISUAL"
	if configured == "" {
		configured, source = os.Getenv("EDITOR"), "$EDITOR"
	}
	if configured != "" {
		if !shellHeadResolves(configured) {
			return "", fmt.Errorf("%s (%s) not found — fix it, or unset it to use a fallback", source, configured)
		}
		return configured, nil
	}
	for _, candidate := range fallbackEditors {
		if systemshell.FindCommand(candidate) != "" {
			return candidate, nil
		}
	}
	return "", errors.New("no editor found — install one or set $EDITOR")
}

func shellHeadResolves(command string) bool {
	command = strings.TrimLeft(command, " \t")
	if command == "" {
		return false
	}
	const shellSyntax = "\"'`\\$;|&<>()=~*?[#"
	for index, value := range command {
		if value == ' ' || value == '\t' {
			head := command[:index]
			if head == "" {
				return false
			}
			return systemshell.FindCommand(head) != ""
		}
		if strings.ContainsRune(shellSyntax, value) {
			return true
		}
	}
	return systemshell.FindCommand(command) != ""
}

func shellQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'" }
