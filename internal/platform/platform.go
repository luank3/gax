// Package platform isolates all OS-specific system interactions (terminal raw
// mode/size, shell resolution, process-tree control, clipboard, XDG directories,
// advisory file locking, keep-awake, and browser launch) behind build tags.
// No other package may import syscall, golang.org/x/sys, or check runtime.GOOS.
package platform

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// ShellSpec describes a resolved shell binary and how to pass a command string.
type ShellSpec struct {
	Path    string
	Name    string // "bash", "sh", "pwsh", "powershell", "cmd"
	Command []string
}

// Dirs holds resolved XDG Base Directory paths for gax.
type Dirs struct {
	Config string
	State  string
	Cache  string
	Home   string
}

// TerminalState restores a terminal's previous mode when closed.
type TerminalState interface {
	Restore() error
}

// FileLock is an open advisory lock on a file.
type FileLock interface {
	Unlock() error
}

// ProcessGroup manages a spawned child command and its descendant processes.
type ProcessGroup interface {
	Wait() error
	SignalGraceful() error
	KillTree() error
	Close() error
}

// ClipboardData represents text or an image read from the system clipboard.
type ClipboardData struct {
	Text      string
	ImagePNG  []byte
	MediaType string
}

// KeepAwake inhibits idle sleep until Stop is called.
type KeepAwake interface {
	Stop()
}

// StartCommand starts cmd inside a dedicated process group / job object.
func StartCommand(cmd *exec.Cmd) (ProcessGroup, error) {
	return startCommand(cmd)
}

// ResolveShell returns the shell to execute commands with, respecting override if non-empty.
func ResolveShell(override string) (ShellSpec, error) {
	return resolveShell(override)
}

// ResolveDirs returns XDG-compliant config, state, and cache directories.
func ResolveDirs() Dirs {
	return resolveDirs()
}

// LockFile acquires an exclusive advisory lock on path, creating it if needed.
func LockFile(path string) (FileLock, error) {
	return lockFile(path)
}

// IsTerminal reports whether f refers to an interactive terminal/console.
func IsTerminal(f *os.File) bool {
	return isTerminal(f)
}

// TerminalSize returns (cols, rows, ok) for the given terminal file.
func TerminalSize(f *os.File) (int, int, bool) {
	return terminalSize(f)
}

// EnterRaw puts in (and optionally out) into raw/VT mode.
func EnterRaw(in, out *os.File, processedSignals bool) (TerminalState, error) {
	return enterRaw(in, out, processedSignals)
}

// ReadByteTimeout waits up to timeout for one byte from f.
func ReadByteTimeout(f *os.File, timeout time.Duration) (byte, bool, error) {
	return readByteTimeout(f, timeout)
}

// ReadClipboard reads an image (if available) or text from the system clipboard.
func ReadClipboard(ctx context.Context) (ClipboardData, error) {
	return readClipboard(ctx)
}

// WriteClipboard copies text to the system clipboard.
func WriteClipboard(ctx context.Context, text string) error {
	return writeClipboard(ctx, text)
}

// InhibitSleep starts best-effort idle-sleep inhibition.
func InhibitSleep(reason string) KeepAwake {
	return inhibitSleep(reason)
}

// OpenURL opens url in the user's default browser.
func OpenURL(ctx context.Context, url string) error {
	return openURL(ctx, url)
}
