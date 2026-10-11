//go:build !linux && !darwin

package width

import "io"

// WriterColumns reports that platform-specific terminal sizing is unavailable.
func WriterColumns(io.Writer) (int, bool) { return 0, false }

// TerminalColumns returns the upstream fallback where this build has no tty-size probe.
func TerminalColumns() int { return fallbackWidth }
