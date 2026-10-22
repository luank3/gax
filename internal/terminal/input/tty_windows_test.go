//go:build windows

package input

import (
	"context"
	"os"
	"syscall"
	"testing"
	"unsafe"

	"github.com/luank3/gax/internal/system/console"
)

func windowsTestConsole(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	kernel := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = kernel.NewProc("FreeConsole").Call()
	result, _, allocErr := kernel.NewProc("AllocConsole").Call()
	if result == 0 {
		t.Skipf("isolated console unavailable: %v", allocErr)
	}
	t.Cleanup(func() { _, _, _ = kernel.NewProc("FreeConsole").Call() })
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("test console unavailable: %v", err)
	}
	t.Cleanup(func() { in.Close() })
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	console.Flush(in)
	return in, out
}

func windowsTestKeys(t *testing.T, in *os.File, text string) {
	t.Helper()
	type record struct {
		kind                   uint16
		_                      uint16
		down                   int32
		repeat, vk, scan, char uint16
		control                uint32
	}
	var records []record
	for _, char := range text {
		records = append(records, record{kind: 1, down: 1, repeat: 1, char: uint16(char)})
	}
	var written uint32
	result, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("WriteConsoleInputW").Call(in.Fd(), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&written)))
	if result == 0 || int(written) != len(records) {
		t.Fatalf("WriteConsoleInputW(%q) = %d,%v", text, written, err)
	}
}

func TestWindowsTTYEditorAndModalRestore(t *testing.T) {
	in, out := windowsTestConsole(t)
	var original uint32
	if err := syscall.GetConsoleMode(syscall.Handle(in.Fd()), &original); err != nil {
		t.Fatal(err)
	}
	sawCooked := false
	windowsTestKeys(t, in, "hi\x14!\r")
	text, submitted, err := ReadTTYLine(context.Background(), in, out, &Core{}, nil, "> ", EditorOptions{ModalKeys: map[byte]func(){0x14: func() {
		var mode uint32
		_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &mode)
		sawCooked = mode == original
	}}})
	if err != nil || !submitted || text != "hi!" || !sawCooked {
		t.Errorf("ReadTTYLine() = %q,%v,%v cooked=%v", text, submitted, err, sawCooked)
	}
	var mode uint32
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &mode)
	if mode != original {
		t.Errorf("ReadTTYLine() left input mode %#x, want %#x", mode, original)
	}
}

func TestWindowsInterruptWatchStopsAndResolvesEscape(t *testing.T) {
	in, _ := windowsTestConsole(t)
	var original uint32
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &original)
	watch, err := StartInterruptWatch(in, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	windowsTestKeys(t, in, "\x1b\x1b")
	watch.StopResolvePending()
	watch.Stop()
	if !watch.PauseRequested() || !watch.AbortRequested() {
		t.Errorf("Escape state = pause %v, abort %v, want both", watch.PauseRequested(), watch.AbortRequested())
	}
	var mode uint32
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &mode)
	if mode != original {
		t.Errorf("Stop() left mode %#x, want %#x", mode, original)
	}
}
