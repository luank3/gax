// Package filemention implements the modal @file picker used by the terminal editor.
package filemention

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/luank3/gax/internal/system/locale"
	systempath "github.com/luank3/gax/internal/system/path"
	"github.com/luank3/gax/internal/system/spawn"
)

// selectionCapacity includes the trailing NUL slot used by upstream's fixed-size record buffer.
const selectionCapacity = 4096

const candidateCommand = "git ls-files -z --cached --others --exclude-standard 2>/dev/null" +
	" || find . \\( -name .git -o -name node_modules \\) -prune -o -type f -print0 2>/dev/null"

const fzfCommandPrefix = "fzf --read0 --print0 --height=~40% --layout=reverse --scheme=path --query="

type pickerQuery struct {
	root   string
	filter string
}

// Match finds an @token at the beginning of a whitespace-delimited word and returns the span
// between the token's @ and the current cursor.
func Match(buffer string, cursor int) (start, end int, matched bool) {
	if cursor <= 0 || cursor > len(buffer) {
		return 0, 0, false
	}
	tokenStart := cursor
	for tokenStart > 0 && !isSpace(buffer[tokenStart-1]) {
		tokenStart--
	}
	if tokenStart >= cursor || buffer[tokenStart] != '@' {
		return 0, 0, false
	}
	return tokenStart, cursor, true
}

func isSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

// BuildCommand returns the shell pipeline that streams repository files to fzf.
func BuildCommand(queryText string) string {
	query := parseQuery(queryText)
	quotedFilter := shellQuote(query.filter)
	if query.root == "" {
		return "{ " + candidateCommand + "; } | " + fzfCommandPrefix + quotedFilter
	}
	return "{ cd " + shellQuote(expandHome(query.root)) + " 2>/dev/null && { " + candidateCommand +
		"; }; } | " + fzfCommandPrefix + quotedFilter
}

func parseQuery(text string) pickerQuery {
	if !usesExternalRoot(text) {
		return pickerQuery{filter: text}
	}
	slash := strings.LastIndexByte(text, '/')
	if slash < 0 {
		return pickerQuery{root: text + "/"}
	}
	return pickerQuery{root: text[:slash+1], filter: text[slash+1:]}
}

func usesExternalRoot(text string) bool {
	return strings.HasPrefix(text, "/") ||
		strings.HasPrefix(text, "~") && (len(text) == 1 || text[1] == '/') ||
		text == ".." || strings.HasPrefix(text, "../")
}

func shellQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'" }

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home := os.Getenv("HOME")
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return systempath.ExpandHome(path, home)
}

// Available reports whether fzf can be launched from PATH.
func Available() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

// Pick launches the modal selector and returns a selected regular file path. Cancellation is not
// an error; launch, output, and stale-selection failures are reported to the caller.
func Pick(queryText string) (string, bool, error) {
	if runtime.GOOS == "windows" {
		return "", false, errors.New("@file completion requires a POSIX shell")
	}
	if !Available() {
		return "", false, errors.New("@file completion needs fzf installed")
	}
	command := exec.CommandContext(context.Background(), "/bin/sh", "-c", BuildCommand(queryText))
	command.Env = forceUTF8ChildEnvironment(os.Environ())
	command.Stdin = os.Stdin
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	releaseInterrupts := spawn.SuppressParentInterrupts()
	defer releaseInterrupts()
	if err := command.Start(); err != nil {
		return "", false, err
	}
	selection, readErr := io.ReadAll(io.LimitReader(stdout, selectionCapacity+1))
	if readErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return "", false, readErr
	}
	record, validRecord := firstSelectionRecord(selection)
	if !validRecord {
		_ = command.Process.Kill()
		_ = command.Wait()
		return "", false, errors.New("fzf selection is too long")
	}
	if err := command.Wait(); err != nil {
		return "", false, nil
	}
	selection = record
	if len(selection) == 0 {
		return "", false, nil
	}
	selected := string(selection)
	if strings.HasPrefix(selected, "./") {
		selected = selected[2:]
	}
	query := parseQuery(queryText)
	if query.root != "" {
		selected = systempath.Join(query.root, selected)
	}
	expanded := expandHome(selected)
	info, err := os.Stat(expanded)
	if err != nil {
		return "", false, fmt.Errorf("cannot mention '%s': %w", selected, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("cannot mention '%s': not a regular file", selected)
	}
	return selected, true, nil
}

// firstSelectionRecord mirrors upstream's fixed record buffer while accepting EOF after a record.
func firstSelectionRecord(output []byte) ([]byte, bool) {
	if nul := bytes.IndexByte(output, 0); nul >= 0 {
		if nul >= selectionCapacity {
			return nil, false
		}
		return output[:nul], true
	}
	if len(output) >= selectionCapacity {
		return nil, false
	}
	return output, true
}

func forceUTF8ChildEnvironment(environment []string) []string {
	return locale.ForceUTF8ChildEnvironment(environment)
}
