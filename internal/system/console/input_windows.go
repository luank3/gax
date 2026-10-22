//go:build windows

package console

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var readConsoleInput = kernel32.NewProc("ReadConsoleInputW")

type inputRecord struct {
	Type  uint16
	_     uint16
	Event [16]byte
}
type keyEvent struct {
	Down                           int32
	Repeat, VirtualKey, Scan, Char uint16
	Control                        uint32
}

// Source translates native console key records into the editor's UTF-8/ANSI byte contract.
// A source has one reader and owns its pending UTF-8 and escape-sequence bytes.
type Source struct {
	File    *os.File
	pending []byte
	high    uint16
}

func (source *Source) feedKey(key keyEvent) {
	if key.Down == 0 {
		return
	}
	repeat := int(key.Repeat)
	if repeat == 0 {
		repeat = 1
	}
	altgr := key.Control&1 != 0 && key.Control&8 != 0
	alt := key.Control&3 != 0 && !altgr
	ctrl := key.Control&12 != 0 && !altgr
	modifier := 1
	if key.Control&16 != 0 {
		modifier++
	}
	if alt {
		modifier += 2
	}
	if ctrl {
		modifier += 4
	}
	text := ""
	switch key.VirtualKey {
	case 0x25, 0x26, 0x27, 0x28, 0x24, 0x23:
		code := map[uint16]string{0x25: "D", 0x26: "A", 0x27: "C", 0x28: "B", 0x24: "H", 0x23: "F"}[key.VirtualKey]
		if modifier == 1 {
			text = "\x1b[" + code
		} else {
			text = fmt.Sprintf("\x1b[1;%d%s", modifier, code)
		}
	case 0x21, 0x22, 0x2d, 0x2e:
		code := map[uint16]int{0x21: 5, 0x22: 6, 0x2d: 2, 0x2e: 3}[key.VirtualKey]
		if modifier == 1 {
			text = fmt.Sprintf("\x1b[%d~", code)
		} else {
			text = fmt.Sprintf("\x1b[%d;%d~", code, modifier)
		}
	case 0x09:
		if key.Control&16 != 0 {
			text = "\x1b[Z"
		} else {
			text = "\t"
		}
	default:
		char := key.Char
		if ctrl && char == 0 && key.VirtualKey >= 0x40 && key.VirtualKey <= 0x5f {
			char = key.VirtualKey & 0x1f
		}
		if char == 0 {
			return
		}
		if char >= 0xd800 && char <= 0xdbff {
			source.high = char
			return
		}
		if char >= 0xdc00 && char <= 0xdfff && source.high != 0 {
			text = string(utf16.DecodeRune(rune(source.high), rune(char)))
			source.high = 0
		} else {
			if source.high != 0 {
				text = "�"
				source.high = 0
			}
			text += string(rune(char))
		}
		if alt {
			text = "\x1b" + text
		}
	}
	source.pending = append(source.pending, strings.Repeat(text, repeat)...)
}

// ReadByteTimeout waits at most timeout; a nonpositive timeout waits indefinitely.
func (source *Source) ReadByteTimeout(timeout time.Duration) (byte, bool, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		if len(source.pending) > 0 {
			value := source.pending[0]
			source.pending = source.pending[1:]
			return value, true, nil
		}
		wait := uint32(syscall.INFINITE)
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, false, nil
			}
			wait = uint32((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		result, err := syscall.WaitForSingleObject(syscall.Handle(source.File.Fd()), wait)
		if err != nil {
			return 0, false, err
		}
		if result == syscall.WAIT_TIMEOUT {
			return 0, false, nil
		}
		var record inputRecord
		var count uint32
		resultCall, _, err := readConsoleInput.Call(source.File.Fd(), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&count)))
		if resultCall == 0 {
			return 0, false, err
		}
		if count != 0 && record.Type == 1 {
			source.feedKey(*(*keyEvent)(unsafe.Pointer(&record.Event[0])))
		}
	}
}
