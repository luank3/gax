//go:build linux

package input_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"syscall"

	"github.com/luank3/gax/internal/terminal/input"
)

func TestReadTTYLineEnablesEditorAndRestoresTermios(t *testing.T) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "pty-master")
	t.Cleanup(func() { _ = master.Close() })
	unlock := int32(0)
	if err := testIoctl(masterFD, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Skipf("unlock PTY: %v", err)
	}
	var number uint32
	if err := testIoctl(masterFD, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Skipf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.Itoa(int(number))), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if !input.TTYPair(slave, slave, true) {
		t.Fatal("PTY pair was not recognized as a terminal")
	}
	initial, err := testGetTermios(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		line      string
		submitted bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		line, submitted, err := input.ReadTTYLine(ctx, slave, slave, input.NewCore(), nil, "> ")
		done <- result{line: line, submitted: submitted, err: err}
	}()
	enabled := make([]byte, len("\x1b[?2004h"))
	if _, err := io.ReadFull(master, enabled); err != nil {
		t.Fatalf("read bracketed-paste enable sequence: %v", err)
	}
	if string(enabled) != "\x1b[?2004h" {
		t.Fatalf("bracketed-paste enable sequence = %q", enabled)
	}
	if _, err := master.Write([]byte("hello\x1b[D!\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.submitted || got.line != "hell!o" {
			t.Fatalf("ReadTTYLine() = %q, %t, %v", got.line, got.submitted, got.err)
		}
	case <-ctx.Done():
		t.Fatal("ReadTTYLine did not finish after input")
	}
	final, err := testGetTermios(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(final, initial) {
		t.Fatalf("termios was not restored: before=%+v after=%+v", initial, final)
	}
}

func TestReadTTYLineAppliesDisplayAndPasteOptions(t *testing.T) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "pty-master")
	t.Cleanup(func() { _ = master.Close() })
	unlock := int32(0)
	if err := testIoctl(masterFD, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Skipf("unlock PTY: %v", err)
	}
	var number uint32
	if err := testIoctl(masterFD, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Skipf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.Itoa(int(number))), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })

	var output bytes.Buffer
	writer := io.MultiWriter(slave, &output)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pasteCalled := false
	type result struct {
		line      string
		submitted bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		line, submitted, err := input.ReadTTYLine(ctx, slave, writer, input.NewCore(), nil, "> ", input.EditorOptions{
			DisplayColumns: func(int) int { return 5 },
			PasteHook: func() (string, bool) {
				pasteCalled = true
				return "clipboard", true
			},
			PasteFilter: func(text string) (string, bool) {
				if text == "file:///tmp/pic.PNG" {
					return "[pasted image: /tmp/pic.PNG] ", true
				}
				return "", false
			},
		})
		done <- result{line: line, submitted: submitted, err: err}
	}()
	if _, err := io.ReadFull(master, make([]byte, len("\x1b[?2004h"))); err != nil {
		t.Fatalf("read bracketed-paste enable sequence: %v", err)
	}
	if _, err := master.Write([]byte("abcdef\x16\x1b[200~file:///tmp/pic.PNG\x1b[201~\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.submitted || got.line != "abcdefclipboard[pasted image: /tmp/pic.PNG] " || !pasteCalled {
			t.Fatalf("ReadTTYLine() = %q, %t, %v", got.line, got.submitted, got.err)
		}
	case <-ctx.Done():
		t.Fatal("ReadTTYLine did not finish after input")
	}
	if !strings.Contains(output.String(), "\x1b[K\r\n") {
		t.Fatalf("configured options did not wrap the buffer: %q", output.String())
	}
}

func TestReadTTYLineRunsModalActionsInCookedMode(t *testing.T) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "pty-master")
	t.Cleanup(func() { _ = master.Close() })
	unlock := int32(0)
	if err := testIoctl(masterFD, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Skipf("unlock PTY: %v", err)
	}
	var number uint32
	if err := testIoctl(masterFD, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		t.Skipf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.Itoa(int(number))), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	initial, err := testGetTermios(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		line      string
		submitted bool
		err       error
	}
	done := make(chan result, 1)
	modalSawCooked := false
	modalKeySawCooked := false
	editSawCooked := false
	completer := input.Completer{
		Match: func(buffer string, cursor int) (int, int, bool) {
			if cursor > 0 && buffer[0] == '@' {
				return 0, cursor, true
			}
			return 0, 0, false
		},
		Complete: func(token string) (string, bool) {
			current, termiosErr := testGetTermios(int(slave.Fd()))
			modalSawCooked = token == "@query" && termiosErr == nil &&
				current.Lflag&syscall.ICANON != 0 && current.Lflag&syscall.ECHO != 0
			return "selected file.txt", true
		},
		Modal: true,
	}
	options := input.EditorOptions{
		Completers: []input.Completer{completer},
		ModalKeys: map[byte]func(){0x14: func() {
			current, termiosErr := testGetTermios(int(slave.Fd()))
			modalKeySawCooked = termiosErr == nil && current.Lflag&syscall.ICANON != 0 && current.Lflag&syscall.ECHO != 0
		}},
		EditBuffer: func(buffer string) (string, bool) {
			current, termiosErr := testGetTermios(int(slave.Fd()))
			editSawCooked = buffer == "draft" && termiosErr == nil &&
				current.Lflag&syscall.ICANON != 0 && current.Lflag&syscall.ECHO != 0
			return "@query", true
		},
	}
	go func() {
		line, submitted, err := input.ReadTTYLine(ctx, slave, slave, input.NewCore(), nil, "> ", options)
		done <- result{line: line, submitted: submitted, err: err}
	}()
	if _, err := io.ReadFull(master, make([]byte, len("\x1b[?2004h"))); err != nil {
		t.Fatalf("read bracketed-paste enable sequence: %v", err)
	}
	if _, err := master.Write([]byte("draft\x07\t\x14\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.submitted || got.line != "selected file.txt" || !modalSawCooked || !modalKeySawCooked || !editSawCooked {
			t.Fatalf("ReadTTYLine() = %q, %t, %v; modal cooked=%t modal key cooked=%t editor cooked=%t", got.line, got.submitted, got.err, modalSawCooked, modalKeySawCooked, editSawCooked)
		}
	case <-ctx.Done():
		t.Fatal("ReadTTYLine did not finish after modal completion")
	}
	final, err := testGetTermios(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(final, initial) {
		t.Fatalf("termios was not restored after modal completion: before=%+v after=%+v", initial, final)
	}
}

func TestHistoryFileIgnoresFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("create FIFO: %v", err)
	}
	core := input.NewCore()
	history := input.OpenHistory(core, path, true)
	history.Add(core, "prompt")
	if got := core.HistoryEntries(); len(got) != 1 || got[0] != "prompt" {
		t.Fatalf("in-memory history = %#v", got)
	}
}

func testIoctl(fd int, request uintptr, argument unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(argument))
	if errno != 0 {
		return errno
	}
	return nil
}

func testGetTermios(fd int) (syscall.Termios, error) {
	var termios syscall.Termios
	if err := testIoctl(fd, uintptr(syscall.TCGETS), unsafe.Pointer(&termios)); err != nil {
		return syscall.Termios{}, err
	}
	return termios, nil
}
