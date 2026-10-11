package app

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/session"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	sessionPickerLabelCells   = 512
	sessionPickerSubjectCells = 60
	sessionPickerMaxEntries   = 200
)

func pickSession(cwd, excludePath string, store *config.Store, stdin io.Reader, stdinTTY bool, stdout io.Writer) (*session.Entry, bool, error) {
	if !terminalinput.TTYPair(stdin, stdout, stdinTTY) {
		return nil, false, nil
	}
	entries, err := session.List(cwd, store.Int("session_retention_days"))
	if err != nil {
		return nil, false, fmt.Errorf("list conversations: %w", err)
	}
	visible := make([]session.Entry, 0, len(entries))
	for _, entry := range entries {
		if excludePath != "" && entry.Path == excludePath {
			continue
		}
		visible = append(visible, entry)
	}
	if len(visible) == 0 {
		_, err := io.WriteString(stdout, "\x1b[2mno past conversations in this directory\x1b[22m\n")
		return nil, false, err
	}
	count := min(len(visible), sessionPickerMaxEntries)
	items := make([]terminalpicker.Item, count)
	now := time.Now()
	for index := range items {
		entry := visible[index]
		label := session.ReadPickerLabel(entry.Path, sessionPickerLabelCells)
		prompt := label.Prompt
		if prompt == "" {
			prompt = "(no preview)"
		}
		items[index] = terminalpicker.Item{
			Label: prompt, Detail: relativeSessionTime(now.Sub(entry.ModTime)),
			Description: sessionProvenance(label),
		}
	}
	title := "resume a conversation"
	if count < len(visible) {
		title = fmt.Sprintf("resume a conversation · newest %d of %d", count, len(visible))
	}
	options := terminalpicker.Options{
		Title: title, Items: items, RepeatClippedLabel: true,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}
	stdinFile, _ := stdin.(*os.File)
	index, opened, err := terminalpicker.RunTTY(options, stdinFile, stdout)
	if err != nil || index < 0 || index >= count {
		return nil, opened, err
	}
	selected := visible[index]
	return &selected, opened, nil
}

func relativeSessionTime(age time.Duration) string {
	seconds := int64(math.Floor(age.Seconds()))
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return "just now"
	case seconds < 60*60:
		return fmt.Sprintf("%dm ago", seconds/60)
	case seconds < 24*60*60:
		return fmt.Sprintf("%dh ago", seconds/3600)
	default:
		return fmt.Sprintf("%dd ago", seconds/86400)
	}
}

func sessionProvenance(label session.PickerLabel) string {
	var selection []string
	for _, value := range []string{label.Provider, label.Model, label.Effort} {
		if value = terminalwidth.FlattenForDisplay(value); value != "" {
			selection = append(selection, value)
		}
	}
	selectionLine := strings.Join(selection, " · ")
	if preset := terminalwidth.FlattenForDisplay(label.Preset); preset != "" {
		if selectionLine == "" {
			selectionLine = "[" + preset + "]"
		} else {
			selectionLine = "[" + preset + "] " + selectionLine
		}
	}

	var git []string
	if branch := terminalwidth.FlattenForDisplay(label.GitBranch); branch != "" {
		git = append(git, branch)
	}
	if subject := terminalwidth.FlattenForDisplay(label.GitSubject); subject != "" {
		git = append(git, terminalwidth.TruncateForDisplay(subject, sessionPickerSubjectCells))
	}
	gitLine := strings.Join(git, " · ")
	switch {
	case selectionLine == "":
		return gitLine
	case gitLine == "":
		return selectionLine
	default:
		return selectionLine + "\n" + gitLine
	}
}
