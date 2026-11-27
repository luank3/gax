//go:build windows

package platform

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type windowsTerminalState struct {
	in      windows.Handle
	out     windows.Handle
	inMode  uint32
	outMode uint32
	hasIn   bool
	hasOut  bool
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

func terminalSize(f *os.File) (int, int, bool) {
	if f == nil {
		return 0, 0, false
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return 0, 0, false
	}
	cols := int(info.Window.Right - info.Window.Left + 1)
	rows := int(info.Window.Bottom - info.Window.Top + 1)
	if cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return cols, rows, true
}

func enterRaw(in, out *os.File, processedSignals bool) (TerminalState, error) {
	st := &windowsTerminalState{}
	if in != nil {
		h := windows.Handle(in.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			return nil, err
		}
		st.in = h
		st.inMode = mode
		st.hasIn = true

		raw := (mode &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_MOUSE_INPUT | windows.ENABLE_QUICK_EDIT_MODE)) |
			windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		if processedSignals {
			raw |= windows.ENABLE_PROCESSED_INPUT
		} else {
			raw &^= windows.ENABLE_PROCESSED_INPUT
		}
		if err := windows.SetConsoleMode(h, raw); err != nil {
			return nil, err
		}
	}
	if out != nil {
		h := windows.Handle(out.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err == nil {
			st.out = h
			st.outMode = mode
			st.hasOut = true
			_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
	return st, nil
}

func (s *windowsTerminalState) Restore() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.hasIn {
		if err := windows.SetConsoleMode(s.in, s.inMode); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.hasOut {
		if err := windows.SetConsoleMode(s.out, s.outMode); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func readByteTimeout(f *os.File, timeout time.Duration) (byte, bool, error) {
	if f == nil {
		return 0, false, nil
	}
	event, err := windows.WaitForSingleObject(windows.Handle(f.Fd()), uint32(timeout.Milliseconds()))
	if err != nil || event != windows.WAIT_OBJECT_0 {
		return 0, false, err
	}
	var buf [1]byte
	nr, rerr := f.Read(buf[:])
	if nr == 1 {
		return buf[0], true, nil
	}
	return 0, false, rerr
}
