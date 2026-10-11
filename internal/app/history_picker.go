package app

import (
	"fmt"
	"io"
	"os"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/slash"
	terminalpicker "github.com/luank3/gax/internal/terminal/picker"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const historyPickerLabelCells = 512

func pickHistoryTurn(state *agent.Session, action string, store *config.Store, stdin io.Reader,
	stdout io.Writer) (int, bool, error) {
	if state == nil {
		return -1, false, nil
	}
	turns := typedUserTurnCount(state.Items)
	if turns == 0 {
		return -1, false, nil
	}
	items := make([]terminalpicker.Item, 0, turns)
	for _, item := range state.Items {
		if !conversation.IsTypedPrompt(item) {
			continue
		}
		label := ""
		if item.Text != nil {
			label = *item.Text
		}
		label = terminalwidth.TruncateForDisplay(terminalwidth.FlattenForDisplay(label), historyPickerLabelCells)
		if label == "" {
			label = "(empty)"
		}
		items = append(items, terminalpicker.Item{Label: label})
	}
	title := "revert to before which message"
	if action == "fork" {
		title = "fork before which message"
	}
	options := terminalpicker.Options{
		Title: title, Items: items, InitialIndex: len(items) - 1, RepeatClippedLabel: true,
		DisplayColumns: func(columns int) int { return editorDisplayColumns(store, columns) },
	}
	stdinFile, _ := stdin.(*os.File)
	index, opened, err := terminalpicker.RunTTY(options, stdinFile, stdout)
	return index, opened, err
}

func pickHistoryCommand(parsed slash.Parsed, state *agent.Session, store *config.Store, stdin io.Reader,
	stdout, stderr io.Writer, interactive bool) (slash.Parsed, bool) {
	if parsed.HasArgument || !interactive || typedUserTurnCount(state.Items) == 0 {
		return parsed, false
	}
	index, _, err := pickHistoryTurn(state, parsed.Command.Name, store, stdin, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "gax: /%s picker: %v\n", parsed.Command.Name, err)
		return parsed, true
	}
	if index < 0 {
		return parsed, true
	}
	parsed.Argument = fmt.Sprintf("%d", typedUserTurnCount(state.Items)-index)
	parsed.HasArgument = true
	return parsed, false
}
