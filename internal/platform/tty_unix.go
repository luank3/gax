//go:build !windows

package platform

import (
	"os"

	"golang.org/x/sys/unix"
)

type unixTerminalState struct {
	fd      int
	termios unix.Termios
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

func terminalSize(f *os.File) (int, int, bool) {
	if f == nil {
		return 0, 0, false
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}

func enterRaw(in, _ *os.File, processedSignals bool) (TerminalState, error) {
	fd := int(in.Fd())
	orig, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *orig
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN
	if !processedSignals {
		raw.Lflag &^= unix.ISIG
	} else {
		raw.Lflag |= unix.ISIG
	}
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return &unixTerminalState{fd: fd, termios: *orig}, nil
}

func (s *unixTerminalState) Restore() error {
	if s == nil {
		return nil
	}
	return unix.IoctlSetTermios(s.fd, ioctlSetTermios, &s.termios)
}
