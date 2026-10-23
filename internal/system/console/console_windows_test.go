//go:build windows

package console

import (
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestKeyDecoding(t *testing.T) {
	for _, test := range []struct {
		name string
		key  keyEvent
		want string
	}{
		{"text", keyEvent{Down: 1, Repeat: 1, Char: '界'}, "界"},
		{"up", keyEvent{Down: 1, Repeat: 1, VirtualKey: 0x26}, "\x1b[A"},
		{"ctrl-left", keyEvent{Down: 1, Repeat: 1, VirtualKey: 0x25, Control: 8}, "\x1b[1;5D"},
		{"alt-b", keyEvent{Down: 1, Repeat: 1, Char: 'b', Control: 2}, "\x1bb"},
		{"altgr", keyEvent{Down: 1, Repeat: 1, Char: '€', Control: 1 | 8}, "€"},
		{"release", keyEvent{Down: 0, Char: 'x'}, ""},
		{"repeat", keyEvent{Down: 1, Repeat: 3, Char: 'x'}, "xxx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := Source{}
			source.feedKey(test.key)
			if string(source.pending) != test.want {
				t.Errorf("feedKey(%+v) = %q, want %q", test.key, source.pending, test.want)
			}
		})
	}
	source := Source{}
	source.feedKey(keyEvent{Down: 1, Repeat: 1, Char: 0xd83d})
	source.feedKey(keyEvent{Down: 1, Repeat: 1, Char: 0xde00})
	if !reflect.DeepEqual(source.pending, []byte("😀")) {
		t.Errorf("feedKey(surrogate pair) = %q, want 😀", source.pending)
	}
}

func TestConsoleRejectsPipes(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if IsTerminal(reader) {
		t.Error("IsTerminal(pipe) = true")
	}
	if _, err := Raw(reader, writer, false); err == nil {
		t.Error("Raw(pipe) succeeded")
	}
	if _, _, ok := Size(writer); ok {
		t.Error("Size(pipe) succeeded")
	}
}

func TestRawRestoresConsoleAndReadsNativeKeys(t *testing.T) {
	_, _, _ = kernel32.NewProc("FreeConsole").Call()
	result, _, allocErr := kernel32.NewProc("AllocConsole").Call()
	if result == 0 {
		t.Skipf("isolated console unavailable: %v", allocErr)
	}
	defer kernel32.NewProc("FreeConsole").Call()
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var beforeInput, beforeOutput uint32
	if err := syscall.GetConsoleMode(syscall.Handle(in.Fd()), &beforeInput); err != nil {
		t.Fatal(err)
	}
	beforeInput |= 0x10 | 0x40 | 0x80 // mouse input, Quick Edit, extended flags
	if err := setMode(in, beforeInput); err != nil {
		t.Fatal(err)
	}
	if err := syscall.GetConsoleMode(syscall.Handle(out.Fd()), &beforeOutput); err != nil {
		t.Fatal(err)
	}
	output, err := Output(out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Restore()
	mode, err := Raw(in, out, false)
	if err != nil {
		t.Fatal(err)
	}
	defer mode.Restore()
	var current uint32
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &current)
	if current&7 != 0 {
		t.Errorf("Raw() input mode = %#x, want echo/line/processed disabled", current)
	}
	if current&0x10 != 0 {
		t.Errorf("Raw(processed=false) input mode = %#x, want mouse reporting disabled for native scrolling", current)
	}
	Flush(in)
	record := inputRecord{Type: 1}
	*(*keyEvent)(unsafe.Pointer(&record.Event[0])) = keyEvent{Down: 1, Repeat: 1, Char: '界'}
	var written uint32
	result, _, err = kernel32.NewProc("WriteConsoleInputW").Call(in.Fd(), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&written)))
	if result == 0 || written != 1 {
		t.Fatalf("WriteConsoleInputW = %v,%v", written, err)
	}
	source := Source{File: in}
	var data []byte
	for range len([]byte("界")) {
		value, available, err := source.ReadByteTimeout(time.Second)
		if err != nil || !available {
			t.Fatalf("ReadByteTimeout(native input) = %v,%v", available, err)
		}
		data = append(data, value)
	}
	if string(data) != "界" {
		t.Errorf("console input = %q, want 界", data)
	}
	if _, available, err := source.ReadByteTimeout(time.Millisecond); available || err != nil {
		t.Errorf("ReadByteTimeout(empty) = %v,%v", available, err)
	}
	if columns, rows, ok := Size(out); !ok || columns < 1 || rows < 1 {
		t.Errorf("Size(console) = %d,%d,%v", columns, rows, ok)
	}
	mode.Restore()
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &current)
	if current != beforeInput {
		t.Errorf("Restore() input mode = %#x, want %#x", current, beforeInput)
	}
	_ = syscall.GetConsoleMode(syscall.Handle(out.Fd()), &current)
	if current&4 == 0 {
		t.Errorf("prompt restore disabled VT output between turns: %#x", current)
	}
	output.Restore()
	_ = syscall.GetConsoleMode(syscall.Handle(out.Fd()), &current)
	if current != beforeOutput {
		t.Errorf("Restore() output mode = %#x, want %#x", current, beforeOutput)
	}
	output, err = Output(out)
	if err != nil {
		t.Fatal(err)
	}
	mode, err = Raw(in, out, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &current)
	if current&0x10 != 0 || current&1 == 0 {
		t.Errorf("Raw(processed=true) input mode = %#x, want mouse reporting disabled and Ctrl+C processing enabled", current)
	}
	RestoreAll()
	mode.Restore()
	output.Restore()
	_ = syscall.GetConsoleMode(syscall.Handle(in.Fd()), &current)
	if current != beforeInput {
		t.Errorf("RestoreAll() input = %#x, want %#x", current, beforeInput)
	}
	_ = syscall.GetConsoleMode(syscall.Handle(out.Fd()), &current)
	if current != beforeOutput {
		t.Errorf("RestoreAll() output = %#x, want %#x", current, beforeOutput)
	}
}
