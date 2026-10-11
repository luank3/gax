package input

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestReflowPhysicalRows(t *testing.T) {
	tests := []struct {
		name   string
		widths []int
		cols   int
		want   int
	}{
		{name: "fitting", widths: []int{2, 4, 5}, cols: 5, want: 3},
		{name: "wrapped", widths: []int{9, 10, 1}, cols: 5, want: 5},
		{name: "empty rows", widths: []int{0, 0}, cols: 5, want: 2},
		{name: "empty input", cols: 5, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reflowPhysicalRows(test.widths, test.cols); got != test.want {
				t.Fatalf("reflowPhysicalRows(%v, %d) = %d, want %d", test.widths, test.cols, got, test.want)
			}
		})
	}
}

func TestResizeRepositionsCursorAfterTerminalReflow(t *testing.T) {
	var screen bytes.Buffer
	core := NewCore()
	core.SetBuffer("abcdefghij")
	editor := Editor{Core: core, Writer: &screen, Columns: 10, Rows: 24}
	if err := editor.paint(); err != nil {
		t.Fatal(err)
	}
	if editor.paintedCursorRow != 1 || editor.paintedRows != 2 {
		t.Fatalf("initial paint state = cursor row %d, rows %d; want 1, 2", editor.paintedCursorRow, editor.paintedRows)
	}
	if !editor.resize(5, 24) {
		t.Fatal("resize did not report changed geometry")
	}
	if editor.paintedCursorRow != 2 || editor.paintedRows != 0 {
		t.Fatalf("resize paint state = cursor row %d, rows %d; want 2, 0", editor.paintedCursorRow, editor.paintedRows)
	}
	if err := editor.paint(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(screen.Bytes(), []byte("\x1b[2A")) {
		t.Fatalf("repaint did not climb over reflowed rows: %q", screen.String())
	}
}

func TestResizeClampsCursorWhenRowsShrink(t *testing.T) {
	core := NewCore()
	core.SetBuffer("a\nb\nc")
	editor := Editor{Core: core, Writer: io.Discard, Columns: 80, Rows: 24}
	if err := editor.paint(); err != nil {
		t.Fatal(err)
	}
	if editor.paintedCursorRow != 2 {
		t.Fatalf("initial cursor row = %d, want 2", editor.paintedCursorRow)
	}
	if !editor.resize(80, 2) {
		t.Fatal("row resize did not report changed geometry")
	}
	if editor.paintedCursorRow != 1 || editor.paintedRows != 3 {
		t.Fatalf("row-shrink paint state = cursor row %d, rows %d; want 1, 3", editor.paintedCursorRow, editor.paintedRows)
	}
}

func TestEditorRefreshesGeometryBeforeApplyingNextKey(t *testing.T) {
	var screen bytes.Buffer
	sizeCalls := 0
	editor := Editor{
		Core: NewCore(), Reader: &resizeByteSource{values: []byte("ab\r")}, Writer: &screen,
		Columns: 10, Rows: 24,
		terminalSize: func() (int, int) {
			sizeCalls++
			if sizeCalls < 2 {
				return 10, 24
			}
			return 5, 24
		},
	}
	line, submitted, err := editor.ReadLine()
	if err != nil || !submitted || line != "ab" {
		t.Fatalf("ReadLine() = %q, %t, %v; want resized input", line, submitted, err)
	}
	if editor.Columns != 5 || sizeCalls < 2 {
		t.Fatalf("geometry after resize = %d columns after %d polls, want 5 columns", editor.Columns, sizeCalls)
	}
}

type resizeByteSource struct {
	values []byte
	index  int
}

func (source *resizeByteSource) ReadByteTimeout(_ time.Duration) (byte, bool, error) {
	if source.index == len(source.values) {
		return 0, false, io.EOF
	}
	value := source.values[source.index]
	source.index++
	return value, true, nil
}
