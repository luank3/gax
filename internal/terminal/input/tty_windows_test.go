//go:build windows

package input

import (
	"context"
	"os"
	"syscall"
	"testing"
	"unicode/utf16"
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
	for _, char := range utf16.Encode([]rune(text)) {
		records = append(records, record{kind: 1, down: 1, repeat: 1, char: char})
	}
	var written uint32
	result, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("WriteConsoleInputW").Call(in.Fd(), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&written)))
	if result == 0 || int(written) != len(records) {
		t.Fatalf("WriteConsoleInputW(%q) = %d,%v", text, written, err)
	}
}

func TestWindowsTTYShiftEnterInsertsNewline(t *testing.T) {
	in, out := windowsTestConsole(t)
	windowsTestKeys(t, in, "left")
	type record struct {
		kind                   uint16
		_                      uint16
		down                   int32
		repeat, vk, scan, char uint16
		control                uint32
	}
	event := record{kind: 1, down: 1, repeat: 1, vk: 0x0d, char: '\r', control: 16}
	var written uint32
	result, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("WriteConsoleInputW").Call(in.Fd(), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&written)))
	if result == 0 || written != 1 {
		t.Fatalf("WriteConsoleInputW(Shift+Enter) = %d,%v", written, err)
	}
	windowsTestKeys(t, in, "right\r")

	text, submitted, err := ReadTTYLine(context.Background(), in, out, &Core{}, nil, "> ")
	if err != nil || !submitted || text != "left\nright" {
		t.Errorf("ReadTTYLine(Shift+Enter) = %q,%v,%v, want %q,true,nil", text, submitted, err, "left\nright")
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

func TestWindowsTTYMultilinePaste(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "CRLF", body: "one界\r\n\r\ntwo😀\r\n", want: "one界\n\ntwo😀\n"},
		{name: "LF", body: "one界\n\ntwo😀\n", want: "one界\n\ntwo😀\n"},
		{name: "CR", body: "one界\r\rtwo😀\r", want: "one界\n\ntwo😀\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			in, out := windowsTestConsole(t)
			var originalInput, originalOutput uint32
			if err := syscall.GetConsoleMode(syscall.Handle(in.Fd()), &originalInput); err != nil {
				t.Fatal(err)
			}
			if err := syscall.GetConsoleMode(syscall.Handle(out.Fd()), &originalOutput); err != nil {
				t.Fatal(err)
			}
			windowsTestKeys(t, in, "prefix\x1b[200~"+test.body+"\x1b[201~!\r")
			filtered := false
			text, submitted, err := ReadTTYLine(context.Background(), in, out, &Core{}, nil, "> ", EditorOptions{
				PasteFilter: func(body string) (string, bool) {
					filtered = true
					if body != test.want {
						t.Errorf("PasteFilter(%q) body = %q, want %q", test.body, body, test.want)
					}
					var mode uint32
					if err := syscall.GetConsoleMode(syscall.Handle(in.Fd()), &mode); err != nil {
						t.Fatal(err)
					}
					if mode&0x200 == 0 || mode&0x10 != 0 {
						t.Errorf("ReadTTYLine() paste input mode = %#x, want VT input enabled and mouse input disabled", mode)
					}
					return "", false
				},
			})
			if want := "prefix" + test.want + "!"; err != nil || !submitted || text != want || !filtered {
				t.Errorf("ReadTTYLine(%q) = %q,%v,%v filtered=%v; want %q,true,nil filtered=true", test.body, text, submitted, err, filtered, want)
			}
			for _, stream := range []struct {
				file *os.File
				want uint32
			}{{in, originalInput}, {out, originalOutput}} {
				var mode uint32
				if err := syscall.GetConsoleMode(syscall.Handle(stream.file.Fd()), &mode); err != nil {
					t.Fatal(err)
				}
				if mode != stream.want {
					t.Errorf("ReadTTYLine() left %s mode %#x, want %#x", stream.file.Name(), mode, stream.want)
				}
			}
		})
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
