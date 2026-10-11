package app

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/luank3/gax/internal/agent"
	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
	"github.com/luank3/gax/internal/session"
	"github.com/luank3/gax/internal/slash"
	coretools "github.com/luank3/gax/internal/tools"
)

func handleSlash(parsed slash.Parsed, stdout, stderr io.Writer, store *config.Store, startNew func() error) bool {
	switch parsed.Outcome {
	case slash.NotCommand:
		return false
	case slash.Unknown:
		fmt.Fprintf(stdout, "unknown command: /%s. type /help for the list.\n", parsed.Name)
	case slash.BadUsage:
		fmt.Fprintf(stdout, "/%s takes no arguments.\n", parsed.Name)
	case slash.Handled:
		if parsed.Command == nil {
			return true
		}
		switch parsed.Command.Name {
		case "help":
			printSlashHelp(stdout, appDisplayWidth(store))
		case "new":
			if parsed.HasArgument {
				fmt.Fprintf(stderr, "gax: /%s with a preset is not available in the line-oriented interface yet.\n", parsed.Name)
			} else if startNew != nil {
				if err := startNew(); err != nil {
					fmt.Fprintf(stderr, "gax: start new conversation: %v\n", err)
				}
			}
		default:
			fmt.Fprintf(stderr, "gax: /%s is not available in the line-oriented interface yet.\n", parsed.Name)
		}
	}
	return true
}

func handleUndo(parsed slash.Parsed, state *agent.Session, log *session.Log, stdout, stderr io.Writer,
	tools *coretools.Registry, inputTTY bool) {
	turns := typedUserTurnCount(state.Items)
	if !parsed.HasArgument {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to undo yet")
		} else if inputTTY {
			writeHistoryNoteLine(stdout, "/undo needs a turn count in the line-oriented interface")
		} else {
			writeHistoryNoteLine(stdout, "/undo needs a number of turns when not interactive")
		}
		return
	}
	turnsBack, err := strconv.ParseInt(strings.TrimSpace(parsed.Argument), 10, 64)
	if err != nil || turnsBack < 1 || turnsBack > int64(turns) {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to undo yet")
		} else {
			fmt.Fprintf(stdout, "/undo takes a number of turns between 1 and %d\n", turns)
		}
		return
	}
	keep := turns - int(turnsBack)
	cut := conversation.UserTurnCut(state.Items, keep)
	if err := log.Undo(keep, cut); err != nil {
		fmt.Fprintln(stderr, "gax: could not record the undo in the session file; conversation left unchanged")
		return
	}
	state.Retire(cut)
	unit := "user turns"
	if turnsBack == 1 {
		unit = "user turn"
	}
	if inputTTY {
		_ = replayRecentTurn(stdout, state, fmt.Sprintf("undid %d %s", turnsBack, unit), nil, nil, tools, true)
	} else {
		fmt.Fprintf(stdout, "undid %d %s\n", turnsBack, unit)
	}
}

func typedUserTurnCount(items []conversation.Item) int {
	count := 0
	for _, item := range items {
		if conversation.IsTypedPrompt(item) {
			count++
		}
	}
	return count
}

func handleFork(parsed slash.Parsed, state *agent.Session, log *session.Log, selection session.Selection,
	stdout, stderr io.Writer, tools *coretools.Registry, inputTTY bool) (*session.Log, error) {
	turns := typedUserTurnCount(state.Items)
	if !parsed.HasArgument {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to fork yet")
		} else if inputTTY {
			writeHistoryNoteLine(stdout, "/fork needs a turn count in the line-oriented interface")
		} else {
			writeHistoryNoteLine(stdout, "/fork needs a number of turns when not interactive")
		}
		return nil, nil
	}
	turnsBack, err := strconv.ParseInt(strings.TrimSpace(parsed.Argument), 10, 64)
	if err != nil || turnsBack < 0 || turnsBack > int64(turns) || turnsBack == 0 && len(state.Items) == 0 {
		if turns == 0 {
			writeHistoryNoteLine(stdout, "nothing to fork yet")
		} else {
			fmt.Fprintf(stdout, "/fork takes a number of turns between 0 and %d\n", turns)
		}
		return nil, nil
	}
	if log == nil || !log.Materialized() {
		fmt.Fprintln(stderr, "gax: /fork needs session recording (it is disabled or unavailable)")
		return nil, nil
	}
	cut := len(state.Items)
	if turnsBack > 0 {
		keep := turns - int(turnsBack)
		cut = conversation.UserTurnCut(state.Items, keep)
	}
	forkPath, err := session.ForkFile(log.Path(), state.Items[:cut], selection)
	if err != nil {
		fmt.Fprintf(stderr, "gax: could not create fork: %v\n", err)
		return nil, nil
	}
	forked, err := session.ResumeLog(forkPath, selection, cut)
	if err != nil {
		_ = os.Remove(forkPath)
		fmt.Fprintf(stderr, "gax: could not open the fork session file: %v\n", err)
		return nil, nil
	}
	if err := log.Close(); err != nil {
		writeWarningDiagnostic(stderr, "could not close the source session file: %v", err)
	}
	for index := 0; index < cut; index++ {
		state.Items[index].Inherited = true
	}
	clear(state.Items[cut:])
	state.Items = state.Items[:cut]
	clear(state.Retired)
	state.Retired = state.Retired[:0]
	state.WorkedMS = 0
	state.LastUserTurnMS = -1
	state.CompactionDeferred = false
	state.ResumeReason = agent.ResumeReasonNone
	if inputTTY {
		_ = replayRecentTurn(stdout, state, "forked", nil, nil, tools, true)
	} else {
		fmt.Fprintln(stdout, "forked conversation")
	}
	return forked, nil
}

func writeHistoryNoteLine(writer io.Writer, text string) {
	writeNoteLine(writer, text)
}
