package app

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/slash"
	"github.com/luank3/gax/internal/terminal/filemention"
	terminalnotify "github.com/luank3/gax/internal/terminal/notify"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
	"github.com/luank3/gax/internal/version"
)

func terminalSafeText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
}

func printSlashHelp(writer io.Writer, columns int) {
	printSlashHelpWithStyle(writer, columns, true)
}

func printSlashHelpWithStyle(writer io.Writer, columns int, styled bool) {
	headingOpen, headingClose := "", ""
	if styled {
		headingOpen, headingClose = "\x1b[1m", "\x1b[0m"
	}
	fmt.Fprintf(writer, "%scommands%s\n", headingOpen, headingClose)
	commands := slash.Commands()
	shortcuts := []struct {
		key, description string
		available        bool
		note             string
	}{
		{"enter", "submit prompt", true, ""},
		{"shift-enter", "insert newline (terminal must be configured to send LF)", true, ""},
		{"esc", "pause after the current step to steer the model", true, ""},
		{"esc esc", "interrupt model or running tool immediately", true, ""},
		{"ctrl-c", "cancel current prompt line", true, ""},
		{"ctrl-d", "quit (on empty prompt)", true, ""},
		{"ctrl-l", "clear screen and redraw prompt", true, ""},
		{"ctrl-g", "edit prompt in $EDITOR", true, ""},
		{"ctrl-o", "view the conversation in $PAGER", true, ""},
		{"ctrl-t", "view model-facing transcript in $PAGER", true, ""},
		{"ctrl-v", "paste image (or text) from clipboard", true, ""},
		{"@ + tab", "pick a project file to mention", filemention.Available(), "(fzf not installed)"},
	}
	labelWidth := 0
	for _, command := range commands {
		if cells := 1 + terminalwidth.Cells(command.Name); cells > labelWidth {
			labelWidth = cells
		}
		if cells := 1 + terminalwidth.Cells(command.Alias); cells > labelWidth {
			labelWidth = cells
		}
	}
	for _, shortcut := range shortcuts {
		if cells := terminalwidth.Cells(shortcut.key); cells > labelWidth {
			labelWidth = cells
		}
	}
	textColumn := labelWidth + 4
	for _, command := range commands {
		summary := command.Summary
		if command.Usage != "" {
			summary += " " + command.Usage
		}
		labelOpen := ""
		if styled {
			labelOpen = terminaltheme.Open(terminaltheme.Chrome)
		}
		writeSlashHelpRow(writer, "/"+command.Name, summary, labelWidth, textColumn, columns, labelOpen, false, styled)
		if command.Alias != "" {
			labelOpen := ""
			if styled {
				labelOpen = terminaltheme.Open(terminaltheme.ChromeDim)
			}
			writeSlashHelpRow(writer, "/"+command.Alias, "alias for /"+command.Name, labelWidth, textColumn, columns, labelOpen, true, styled)
		}
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%sshortcuts%s\n", headingOpen, headingClose)
	for _, shortcut := range shortcuts {
		description := shortcut.description
		if !shortcut.available && shortcut.note != "" {
			description += " " + shortcut.note
		}
		labelOpen := ""
		if styled {
			role := terminaltheme.Chrome
			if !shortcut.available {
				role = terminaltheme.ChromeDim
			}
			labelOpen = terminaltheme.Open(role)
		}
		writeSlashHelpRow(writer, shortcut.key, description, labelWidth, textColumn, columns, labelOpen, !shortcut.available, styled)
	}
}

func writeSlashHelpRow(writer io.Writer, label, summary string, labelWidth, textColumn, columns int, labelOpen string, dimmed, styled bool) {
	fmt.Fprint(writer, "  ")
	if labelOpen != "" {
		fmt.Fprint(writer, labelOpen)
	}
	fmt.Fprint(writer, label)
	if labelOpen != "" {
		fmt.Fprint(writer, "\x1b[0m")
	}
	if columns-textColumn >= 20 {
		fmt.Fprint(writer, strings.Repeat(" ", textColumn-2-terminalwidth.Cells(label)))
		styleOpen := ""
		if dimmed && styled {
			styleOpen = "\x1b[2m"
		}
		writeStyledWrappedRows(writer, summary, textColumn, columns, styleOpen)
		return
	}
	fmt.Fprintln(writer)
	fmt.Fprint(writer, "    ")
	styleOpen := ""
	if dimmed && styled {
		styleOpen = "\x1b[2m"
	}
	writeStyledWrappedRows(writer, summary, 4, columns, styleOpen)
}

func writeStyledWrappedRows(writer io.Writer, text string, indent, columns int, styleOpen string) {
	if styleOpen == "" {
		writeWrappedRows(writer, text, indent, columns)
		return
	}
	for _, row := range terminalwidth.WrapRows(text, indent, columns) {
		fmt.Fprintf(writer, "%s%s\x1b[0m\n", styleOpen, row)
	}
}

func printHelp(writer io.Writer, store *config.Store) {
	printHelpWithStyle(writer, store, terminalnotify.IsTerminalWriter(writer))
}

func printHelpWithStyle(writer io.Writer, store *config.Store, styled bool) {
	columns := appDisplayWidth(store)
	tagline := "a minimalist coding assistant in your terminal"
	name := "gax " + version.Value
	boldOpen, boldClose := "", ""
	chromeOpen, chromeClose := "", ""
	if styled {
		boldOpen, boldClose = "\x1b[1m", "\x1b[0m"
		chromeOpen = terminaltheme.Open(terminaltheme.Chrome)
		if chromeOpen != "" {
			chromeClose = "\x1b[0m"
		}
	}
	if terminalwidth.Cells(name)+3+terminalwidth.Cells(tagline) <= columns {
		fmt.Fprintf(writer, "%s%s%s — %s\n", boldOpen, name, boldClose, tagline)
	} else {
		fmt.Fprintf(writer, "%s%s%s\n", boldOpen, name, boldClose)
		writeWrappedRows(writer, tagline, 0, columns)
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%susage:%s ", boldOpen, boldClose)
	writeWrappedRows(writer, "gax [PRESET] [OPTIONS] [PROMPT...]", len("usage: "), columns)
	fmt.Fprintln(writer)
	writeWrappedRows(writer, "With no arguments, runs an interactive REPL.", 0, columns)
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%soptions%s\n", boldOpen, boldClose)
	options := []struct{ flags, description string }{
		{"-p, --print", "Non-interactive mode. Runs the prompt to completion and prints the final assistant message to stdout. The prompt comes from PROMPT positional arguments (joined with spaces) when given, otherwise from stdin if stdin is not a terminal."},
		{"--json", "Non-interactive JSON output (implies -p): stream the run as JSON records on stdout, one per line, ending with a result record. Format reference: docs/sessions.md."},
		{"-c, --continue", "Resume the most recent conversation in this directory."},
		{"--resume[=ID]", "Resume a past conversation in this directory, restoring its recorded settings: pick from a list, or give a session ID. Works with -p; without a prompt, the conversation continues from where it stopped."},
		{"--no-session", "Don't record this conversation: nothing to resume afterwards, and the prompts you type aren't added to Ctrl-R recall. Earlier sessions and prompts stay readable."},
		{"--raw", "Send only the prompt text — no system prompt, no Environment section, no AGENTS.md, no skills, and no tools. Useful as a barebones chat interface."},
		{"--bare", "Run without project and delegation context — no AGENTS.md, skills, or subagents section. The Environment section, tools, and base system prompt remain (unlike --raw)."},
		{"--provider=NAME", "Select the backend for this run."},
		{"--model=ID", "Select the model for this run."},
		{"--effort=LEVEL", "Select the reasoning effort for this run."},
		{"--preset=NAME", "Apply the named preset — a presets.NAME selection from the config file. Explicit selection flags win over the preset's values. The name may also be given right after 'gax' in place of this flag."},
		{"-h, --help", "Show this help and exit."},
		{"-v, --version", "Show version and exit."},
	}
	flagWidth := 0
	for _, option := range options {
		if cells := terminalwidth.Cells(option.flags); cells > flagWidth {
			flagWidth = cells
		}
	}
	textColumn := flagWidth + 4
	for _, option := range options {
		fmt.Fprint(writer, "  ")
		if chromeOpen != "" {
			fmt.Fprint(writer, chromeOpen)
		}
		fmt.Fprint(writer, option.flags)
		if chromeOpen != "" {
			fmt.Fprint(writer, chromeClose)
		}
		if columns-textColumn >= 20 {
			fmt.Fprint(writer, strings.Repeat(" ", textColumn-2-terminalwidth.Cells(option.flags)))
			writeWrappedRows(writer, option.description, textColumn, columns)
		} else {
			fmt.Fprintln(writer)
			fmt.Fprint(writer, "    ")
			writeWrappedRows(writer, option.description, 4, columns)
		}
	}
	fmt.Fprintln(writer)
	writeWrappedRows(writer, "The selection flags (--provider, --model, --effort, --preset) apply to this run only and take priority over every other source. Configuration and precedence are covered in README.md.", 0, columns)
}

func appDisplayWidth(store *config.Store) int {
	mode, configured := "auto", 0
	if store != nil {
		if value := store.String("display_width"); value != nil {
			mode = *value
		}
		configured = store.Int("display_width")
	}
	return terminalwidth.DisplayWidth(terminalwidth.TerminalColumns(), mode, configured)
}

func writeWrappedRows(writer io.Writer, text string, indent, columns int) {
	for _, row := range terminalwidth.WrapRows(text, indent, columns) {
		fmt.Fprintln(writer, row)
	}
}

func writeNoteLine(writer io.Writer, text string) {
	writeNoteLineWithStyle(writer, text, terminalnotify.IsTerminalWriter(writer))
}

func writeNoteLineWithStyle(writer io.Writer, text string, styled bool) {
	if styled {
		fmt.Fprintf(writer, "\x1b[2m%s\x1b[0m\n", text)
		return
	}
	fmt.Fprintln(writer, text)
}
