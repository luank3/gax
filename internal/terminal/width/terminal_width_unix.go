//go:build linux || darwin

package width

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

type windowSize struct {
	rows, columns uint16
	x, y          uint16
}

// WriterColumns returns a terminal writer's physical width and whether it is an attached
// terminal. The fallback width is returned for a terminal whose driver reports zero columns.
func WriterColumns(writer io.Writer) (int, bool) {
	file, ok := writer.(*os.File)
	if !ok {
		return 0, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0, false
	}
	var size windowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, false
	}
	if size.columns == 0 {
		return fallbackWidth, true
	}
	return int(size.columns), true
}

// TerminalColumns returns stdout's physical width, or the upstream 100-column fallback.
func TerminalColumns() int {
	if columns, ok := WriterColumns(os.Stdout); ok {
		return columns
	}
	return fallbackWidth
}
