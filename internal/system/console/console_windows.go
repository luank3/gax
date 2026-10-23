//go:build windows

// Package console implements the native Windows terminal boundary.
package console

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	setConsoleMode = kernel32.NewProc("SetConsoleMode")
	getScreenInfo  = kernel32.NewProc("GetConsoleScreenBufferInfo")
	flushInput     = kernel32.NewProc("FlushConsoleInputBuffer")
	modes          = struct {
		sync.Mutex
		active []*Mode
	}{}
)

// Mode owns the saved console modes for a prompt, picker or interrupt watcher.
type Mode struct {
	in, out       *os.File
	input, output uint32
}

// IsTerminal distinguishes actual consoles from files, NUL and redirected pipes.
func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(file.Fd()), &mode) == nil
}

func setMode(file *os.File, mode uint32) error {
	result, _, err := setConsoleMode.Call(file.Fd(), uintptr(mode))
	if result == 0 {
		return err
	}
	return nil
}

// Raw disables line editing and echo; processed keeps Ctrl+C handled as a console signal.
func Raw(in, out *os.File, processed bool) (*Mode, error) {
	saved := &Mode{in: in, out: out}
	if !IsTerminal(in) {
		return nil, fmt.Errorf("input terminal is unavailable")
	}
	if err := syscall.GetConsoleMode(syscall.Handle(in.Fd()), &saved.input); err != nil {
		return nil, err
	}
	// VT input preserves paste markers; mouse reporting must stay off for native scrolling.
	input := (saved.input &^ uint32(0x2|0x4|0x10|0x40)) | 0x80 | 0x200 // line, echo, mouse, quick edit; extended flags, VT input
	if !processed {
		input &^= 1
	} else {
		input |= 1
	}
	if out != nil {
		if err := syscall.GetConsoleMode(syscall.Handle(out.Fd()), &saved.output); err != nil {
			return nil, err
		}
		if err := setMode(out, saved.output|0x4); err != nil {
			return nil, err
		}
	}
	if err := setMode(in, input); err != nil {
		if out != nil {
			_ = setMode(out, saved.output)
		}
		return nil, err
	}
	modes.Lock()
	modes.active = append(modes.active, saved)
	modes.Unlock()
	return saved, nil
}

// Output keeps ANSI/VT processing enabled between prompts and provider turns.
func Output(out *os.File) (*Mode, error) {
	saved := &Mode{out: out}
	if out == nil {
		return nil, fmt.Errorf("output terminal is unavailable")
	}
	if err := syscall.GetConsoleMode(syscall.Handle(out.Fd()), &saved.output); err != nil {
		return nil, err
	}
	if err := setMode(out, saved.output|4); err != nil {
		return nil, err
	}
	modes.Lock()
	modes.active = append(modes.active, saved)
	modes.Unlock()
	return saved, nil
}

// Restore reinstates the exact modes saved before entering raw mode.
func (mode *Mode) Restore() {
	if mode == nil {
		return
	}
	modes.Lock()
	defer modes.Unlock()
	index := slices.Index(modes.active, mode)
	if index < 0 {
		return
	}
	mode.restore()
	modes.active = slices.Delete(modes.active, index, index+1)
}

func (mode *Mode) restore() {
	if mode.in != nil {
		_ = setMode(mode.in, mode.input)
	}
	if mode.out != nil {
		_ = setMode(mode.out, mode.output)
	}
}

// RestoreAll unwinds nested console modes before termination, which does not run defers.
func RestoreAll() {
	modes.Lock()
	defer modes.Unlock()
	for index := len(modes.active) - 1; index >= 0; index-- {
		modes.active[index].restore()
	}
	modes.active = nil
}

// Flush discards keystrokes typed while a provider turn was running.
func Flush(in *os.File) {
	if in != nil {
		_, _, _ = flushInput.Call(in.Fd())
	}
}

type coord struct{ X, Y int16 }
type rect struct{ Left, Top, Right, Bottom int16 }
type screenInfo struct {
	Size, Cursor coord
	Attributes   uint16
	Window       rect
	Maximum      coord
}

// Size returns the visible console window, rather than the scrollback buffer size.
func Size(file *os.File) (columns, rows int, ok bool) {
	if !IsTerminal(file) {
		return 0, 0, false
	}
	var info screenInfo
	result, _, _ := getScreenInfo.Call(file.Fd(), uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return 0, 0, false
	}
	return int(info.Window.Right - info.Window.Left + 1), int(info.Window.Bottom - info.Window.Top + 1), true
}
