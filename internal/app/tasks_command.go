package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/slash"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	coretools "github.com/luank3/gax/internal/tools"
)

func runTasksCommand(parsed slash.Parsed, tools *coretools.Registry, disabled bool, store *config.Store,
	stdout, stderr io.Writer) {
	if disabled {
		writeNoteLine(stdout, "background tasks are disabled (no_tasks)")
		return
	}
	if parsed.HasArgument && parsed.Argument != "" {
		argument := parsed.Argument
		if len(argument) < 4 || argument[:4] != "kill" ||
			(len(argument) > 4 && argument[4] != ' ' && argument[4] != '\t') {
			fmt.Fprintln(stdout, "usage: /tasks [kill <id>... | kill all]")
			return
		}
		ids := strings.FieldsFunc(argument[4:], func(character rune) bool { return character == ' ' || character == '\t' })
		all := false
		for _, id := range ids {
			if id == "all" {
				all = true
				break
			}
		}
		if !all && len(ids) == 0 {
			fmt.Fprintln(stdout, "usage: /tasks kill <id>... | kill all")
			return
		}
		stopped := tools.StopTasks(ids, all)
		unit := "tasks"
		if stopped == 1 {
			unit = "task"
		}
		fmt.Fprintf(stdout, "  stopped %d %s\n", stopped, unit)
		return
	}
	tasks := tools.ListTasks()
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "  \x1b[2mno background tasks\x1b[0m")
		return
	}
	columns := appDisplayWidth(store)
	idWidth := 4
	statusWidth := 0
	statuses := make([]string, len(tasks))
	for index, task := range tasks {
		if width := terminalwidth.Cells(task.ID); width > idWidth {
			idWidth = width
		}
		state := fmt.Sprintf("exit %d", task.ExitCode)
		if task.Running {
			state = "running"
		} else if task.TermSignal != 0 {
			state = fmt.Sprintf("signal %d", task.TermSignal)
		}
		statuses[index] = state + " · " + formatDuration(task.Elapsed.Milliseconds())
		if width := terminalwidth.Cells(statuses[index]); width > statusWidth {
			statusWidth = width
		}
	}
	for index, task := range tasks {
		fixedWidth := 2 + idWidth + 2 + statusWidth + 2
		commandWidth := columns - fixedWidth - 1
		if commandWidth < 8 {
			commandWidth = 8
		}
		command := terminalwidth.FlattenForDisplay(task.Command)
		command = terminalwidth.TruncateForDisplay(command, commandWidth)
		fmt.Fprintf(stdout, "  %-*s  %-*s  %s\n", idWidth, terminalSafeText(task.ID),
			statusWidth, statuses[index], command)
	}
}
