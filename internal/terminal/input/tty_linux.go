//go:build linux

package input

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	bracketedPasteEnable  = "\x1b[?2004h"
	bracketedPasteDisable = "\x1b[?2004l"
	pollIn                = 0x0001
)

type pollDescriptor struct {
	fd      int32
	events  int16
	revents int16
}

// TTYPair reports whether both streams are terminal files and stdin accepts termios queries.
func TTYPair(stdin io.Reader, stdout io.Writer, inputTTY bool) bool {
	if !inputTTY {
		return false
	}
	in, ok := stdin.(*os.File)
	if !ok || !isCharacterDevice(in) {
		return false
	}
	out, ok := stdout.(*os.File)
	if !ok || !isCharacterDevice(out) {
		return false
	}
	_, err := getTermios(int(in.Fd()))
	if err != nil {
		return false
	}
	var size terminalWindowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, out.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	return errno == 0
}

// ReadTTYLine enables raw mode for one prompt, then always disables bracketed paste and restores
// the terminal mode before returning to the application loop.
func ReadTTYLine(ctx context.Context, stdin *os.File, stdout io.Writer, core *Core, history *HistoryFile, prompt string, options ...EditorOptions) (string, bool, error) {
	if stdin == nil || stdout == nil {
		return "", false, errors.New("input terminal is unavailable")
	}
	fd := int(stdin.Fd())
	saved, err := getTermios(fd)
	if err != nil {
		return "", false, err
	}
	raw := saved
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.BRKINT
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := setTermios(fd, &raw); err != nil {
		return "", false, err
	}
	defer func() { _ = setTermios(fd, &saved) }()
	if _, err := io.WriteString(stdout, bracketedPasteEnable); err != nil {
		_, _ = io.WriteString(stdout, bracketedPasteDisable)
		return "", false, err
	}
	defer func() { _, _ = io.WriteString(stdout, bracketedPasteDisable) }()
	terminalColumns, rows := queryTerminalSize(stdout, 100, 0)
	columns := editorColumnsForOptions(terminalColumns, options)
	initialText := ""
	emptySubmit := false
	for _, option := range options {
		if option.InitialText != "" {
			initialText = option.InitialText
		}
		emptySubmit = emptySubmit || option.EmptySubmit
	}
	editor := Editor{Core: core, History: history, InitialText: initialText, EmptySubmit: emptySubmit, Prompt: prompt,
		Reader: terminalByteSource{fd: fd}, Writer: stdout, Context: ctx, Columns: columns, Rows: rows}
	for _, option := range options {
		for key, callback := range option.ModalKeys {
			if editor.ModalKeys == nil {
				editor.ModalKeys = make(map[byte]func())
			}
			editor.ModalKeys[key] = callback
		}
	}
	editor.terminalSize = func() (int, int) {
		terminalColumns, rows := queryTerminalSize(stdout, 100, 0)
		return editorColumnsForOptions(terminalColumns, options), rows
	}
	for _, option := range options {
		editor.Completers = append(editor.Completers, option.Completers...)
		if option.EditBuffer != nil {
			editor.EditBuffer = option.EditBuffer
		}
		if option.Hint != nil {
			editor.Hint = option.Hint
		}
		if option.PasteHook != nil {
			editor.PasteHook = option.PasteHook
		}
		if option.PasteFilter != nil {
			editor.PasteFilter = option.PasteFilter
		}
	}
	editor.modalRun = func(run func() (string, bool)) (string, bool, error) {
		if err := editor.eraseEditArea(); err != nil {
			return "", false, err
		}
		if err := setTermios(fd, &saved); err != nil {
			return "", false, err
		}
		text, ok := run()
		if err := setTermios(fd, &raw); err != nil {
			return "", false, err
		}
		if editor.terminalSize != nil {
			columns, rows := editor.terminalSize()
			editor.resize(columns, rows)
		}
		return text, ok, nil
	}
	return editor.ReadLine()
}

func isCharacterDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func getTermios(fd int) (syscall.Termios, error) {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		return syscall.Termios{}, errno
	}
	return termios, nil
}

func setTermios(fd int, termios *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(termios)))
	if errno != 0 {
		return errno
	}
	return nil
}

type terminalByteSource struct{ fd int }

type terminalWindowSize struct {
	Rows    uint16
	Columns uint16
	XPixel  uint16
	YPixel  uint16
}

func queryTerminalSize(writer io.Writer, fallbackColumns, fallbackRows int) (int, int) {
	out, ok := writer.(*os.File)
	if !ok {
		return fallbackColumns, fallbackRows
	}
	var size terminalWindowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, out.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return fallbackColumns, fallbackRows
	}
	columns := fallbackColumns
	if size.Columns > 0 {
		columns = int(size.Columns)
	}
	return columns, int(size.Rows)
}

func editorColumnsForOptions(terminalColumns int, options []EditorOptions) int {
	columns := terminalColumns
	for _, option := range options {
		if option.DisplayColumns != nil {
			columns = option.DisplayColumns(terminalColumns)
			break
		}
	}
	if terminalColumns > 0 && columns > terminalColumns {
		columns = terminalColumns
	}
	if columns <= 0 {
		return 1
	}
	return columns
}

func (source terminalByteSource) ReadByteTimeout(timeout time.Duration) (byte, bool, error) {
	var wait *syscall.Timespec
	if timeout > 0 {
		nanos := timeout.Nanoseconds()
		wait = &syscall.Timespec{Sec: nanos / int64(time.Second), Nsec: nanos % int64(time.Second)}
	}
	for {
		descriptor := pollDescriptor{fd: int32(source.fd), events: pollIn}
		result, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&descriptor)), 1,
			uintptr(unsafe.Pointer(wait)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return 0, false, errno
		}
		if result == 0 {
			return 0, false, nil
		}
		var one [1]byte
		n, readErr := syscall.Read(source.fd, one[:])
		if readErr == syscall.EINTR {
			continue
		}
		if readErr != nil {
			return 0, false, readErr
		}
		if n == 0 {
			return 0, false, io.EOF
		}
		return one[0], true, nil
	}
}
