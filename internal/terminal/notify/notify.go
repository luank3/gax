// Package notify emits terminal attention notifications when the interactive REPL becomes idle.
package notify

import (
	"io"
	"os"
	"strings"

	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

type Method string

const (
	Disabled Method = "disabled"
	Bell     Method = "bell"
	OSC9     Method = "osc9"
)

// Environment contains the terminal variables used to choose a notification protocol.
type Environment struct {
	Term                string
	TermProgram         string
	GhosttyResourcesDir string
	GhosttyBinDir       string
	WezTermExecutable   string
	WezTermPane         string
	Tmux                string
}

// CurrentEnvironment returns the terminal variables consulted by auto mode.
func CurrentEnvironment() Environment {
	return Environment{
		Term:                os.Getenv("TERM"),
		TermProgram:         os.Getenv("TERM_PROGRAM"),
		GhosttyResourcesDir: os.Getenv("GHOSTTY_RESOURCES_DIR"),
		GhosttyBinDir:       os.Getenv("GHOSTTY_BIN_DIR"),
		WezTermExecutable:   os.Getenv("WEZTERM_EXECUTABLE"),
		WezTermPane:         os.Getenv("WEZTERM_PANE"),
		Tmux:                os.Getenv("TMUX"),
	}
}

// Select chooses the notification method using the configured value and terminal capabilities.
func Select(configured string, isTerminal bool, env Environment) Method {
	if !isTerminal {
		return Disabled
	}
	switch {
	case strings.EqualFold(configured, "off"):
		return Disabled
	case strings.EqualFold(configured, "bel"):
		return Bell
	case strings.EqualFold(configured, "osc9"):
		return OSC9
	}
	if env.Term == "dumb" {
		return Disabled
	}
	if env.Tmux != "" {
		return Bell
	}
	if supportsOSC9(env) {
		return OSC9
	}
	return Bell
}

// Attention emits the configured notification when the output is a terminal.
func Attention(output io.Writer, isTerminal bool, configured string, env Environment) error {
	method := Select(configured, isTerminal, env)
	var notification string
	switch method {
	case Disabled:
		return nil
	case Bell:
		notification = "\a"
	case OSC9:
		notification = "\x1b]9;gax: ready\a"
		if env.Tmux != "" {
			notification = "\x1bPtmux;\x1b" + notification + "\x1b\\"
		}
	default:
		return nil
	}
	n, err := io.WriteString(output, notification)
	if err != nil {
		return err
	}
	if n != len(notification) {
		return io.ErrShortWrite
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// IsTerminalWriter reports whether output is backed by a terminal character device.
func IsTerminalWriter(output io.Writer) bool {
	_, ok := terminalwidth.WriterColumns(output)
	return ok
}

func supportsOSC9(env Environment) bool {
	switch env.TermProgram {
	case "iTerm.app", "ghostty", "WezTerm", "WarpTerminal":
		return true
	}
	return env.GhosttyResourcesDir != "" || env.GhosttyBinDir != "" ||
		env.WezTermExecutable != "" || env.WezTermPane != ""
}
