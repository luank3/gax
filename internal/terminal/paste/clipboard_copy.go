package paste

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/luank3/gax/internal/system/spawn"
)

const maxOSC52ClipboardBytes = 100000

// CopyClipboard copies text using the platform clipboard helpers and falls back to OSC 52.
// The OSC 52 sequence is written to /dev/tty when available, or fallback otherwise.
func CopyClipboard(ctx context.Context, text string, fallback io.Writer) error {
	ctx, cancel := clipboardContext(ctx)
	defer cancel()
	if isSSHSession() {
		sequence, err := ClipboardOSC52Sequence(text, isTmuxSession())
		if err != nil {
			return errors.New("response too large for OSC 52 over SSH")
		}
		if err := writeOSC52(sequence, fallback); err != nil {
			return errors.New("terminal did not accept OSC 52 sequence")
		}
		return nil
	}
	if copyWithNativeHelper(ctx, text) {
		return nil
	}
	if sequence, err := ClipboardOSC52Sequence(text, isTmuxSession()); err == nil {
		if writeOSC52(sequence, fallback) == nil {
			return nil
		}
	}
	return errors.New("no clipboard helper available (install pbcopy / wl-copy / xclip / xsel, or use a terminal that supports OSC 52)")
}

// ClipboardOSC52Sequence encodes a clipboard string as an OSC 52 terminal sequence.
func ClipboardOSC52Sequence(text string, tmuxWrap bool) ([]byte, error) {
	if len(text) > maxOSC52ClipboardBytes {
		return nil, fmt.Errorf("text exceeds OSC 52 limit of %d bytes", maxOSC52ClipboardBytes)
	}
	sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if tmuxWrap {
		sequence = "\x1bPtmux;\x1b\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07\x1b\\"
	}
	return []byte(sequence), nil
}

func isSSHSession() bool {
	_, tty := os.LookupEnv("SSH_TTY")
	_, connection := os.LookupEnv("SSH_CONNECTION")
	return tty || connection
}

func isTmuxSession() bool {
	_, present := os.LookupEnv("TMUX")
	return present
}

func copyWithNativeHelper(ctx context.Context, text string) bool {
	if copyPlatformClipboard(ctx, text) {
		return true
	}
	if runClipboardWriter(ctx, text, "pbcopy") {
		return true
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" && runClipboardWriter(ctx, text, "wl-copy") {
		return true
	}
	if runClipboardWriter(ctx, text, "xclip", "-selection", "clipboard") {
		return true
	}
	return runClipboardWriter(ctx, text, "xsel", "-b", "-i")
}

func runClipboardWriter(ctx context.Context, text, name string, args ...string) bool {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(text)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	releaseInterrupts := spawn.SuppressParentInterrupts()
	defer releaseInterrupts()
	return command.Run() == nil
}

func writeOSC52(sequence []byte, fallback io.Writer) error {
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err == nil {
		defer terminal.Close()
		return writeAll(terminal, sequence)
	}
	if fallback == nil {
		return err
	}
	return writeAll(fallback, sequence)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
