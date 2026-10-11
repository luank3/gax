// Package diag writes diagnostics to a caller-provided stderr stream.
package diag

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	terminalnotify "github.com/luank3/gax/internal/terminal/notify"
	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

var (
	diagnosticSequence atomic.Uint64
	diagnosticMu       sync.Mutex
)

// Sequence returns the number of diagnostics emitted by this process.
func Sequence() uint64 {
	return diagnosticSequence.Load()
}

// Warn writes one warning diagnostic and advances the process diagnostic sequence.
func Warn(writer io.Writer, format string, values ...any) {
	emit(writer, terminaltheme.Warn, format, values...)
}

// Error writes one error diagnostic and advances the process diagnostic sequence.
func Error(writer io.Writer, format string, values ...any) {
	emit(writer, terminaltheme.Error, format, values...)
}

func emit(writer io.Writer, role terminaltheme.Role, format string, values ...any) {
	if writer == nil {
		return
	}
	message := fmt.Sprintf(format, values...)
	if terminalnotify.IsTerminalWriter(writer) {
		if open := terminaltheme.Open(role); open != "" {
			message = open + "gax: " + message + "\x1b[0m\n"
		} else {
			message = "gax: " + message + "\n"
		}
	} else {
		message = "gax: " + message + "\n"
	}

	diagnosticMu.Lock()
	defer diagnosticMu.Unlock()
	_, _ = io.WriteString(writer, message)
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	diagnosticSequence.Add(1)
}
