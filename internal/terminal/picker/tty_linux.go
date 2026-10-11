//go:build linux

package picker

import (
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"

	terminalocale "github.com/luank3/gax/internal/system/locale"
	terminalinput "github.com/luank3/gax/internal/terminal/input"
)

const pollIn = 0x0001

type pollDescriptor struct {
	fd      int32
	events  int16
	revents int16
}

type windowSize struct {
	rows    uint16
	columns uint16
	xpixel  uint16
	ypixel  uint16
}

// RunTTY opens the picker when stdin and stdout are terminals. opened reports whether
// interactive picker handling took over the current line, including setup failures.
func RunTTY(options Options, stdin *os.File, stdout io.Writer) (index int, opened bool, err error) {
	if stdin == nil || !terminalinput.TTYPair(stdin, stdout, true) {
		return -1, false, nil
	}
	if len(options.Items) == 0 {
		index, err := Run(options, nil, stdout, nil, terminalocale.HaveUTF8())
		return index, false, err
	}
	out, ok := stdout.(*os.File)
	if !ok {
		return -1, false, nil
	}
	saved, err := getTermios(int(stdin.Fd()))
	if err != nil {
		return -1, true, err
	}
	raw := saved
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.BRKINT
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := setTermios(int(stdin.Fd()), &raw); err != nil {
		return -1, true, err
	}
	defer func() { _ = setTermios(int(stdin.Fd()), &saved) }()
	index, err = Run(options, ttyByteSource{fd: int(stdin.Fd())}, stdout, func() (int, int) {
		return getWindowSize(int(out.Fd()))
	}, terminalocale.HaveUTF8())
	return index, true, err
}

type ttyByteSource struct{ fd int }

func (source ttyByteSource) ReadByteTimeout(timeout time.Duration) (byte, bool, error) {
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
		n, err := syscall.Read(source.fd, one[:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		if n == 0 {
			return 0, false, io.EOF
		}
		return one[0], true, nil
	}
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

func getWindowSize(fd int) (int, int) {
	var size windowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 80, 24
	}
	columns, rows := int(size.columns), int(size.rows)
	if columns <= 0 {
		columns = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return columns, rows
}
