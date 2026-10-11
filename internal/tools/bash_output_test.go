package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/system/tempfiles"
)

func TestCommandCaptureKeepsSmallOutputInMemory(t *testing.T) {
	if err := tempfiles.Cleanup(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	capture, err := newCommandCapture(16)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.closeRemove()

	if _, err := capture.Write([]byte("small output\n")); err != nil {
		t.Fatal(err)
	}
	if capture.path != "" {
		t.Fatalf("small output unexpectedly spilled to %q", capture.path)
	}
	data, total, stored, _, _, _ := capture.snapshot(0)
	if string(data) != "small output\n" || total != int64(len(data)) || stored != total {
		t.Fatalf("memory capture = %q, total=%d, stored=%d", data, total, stored)
	}
}

func TestCommandCaptureSpillsToSharedTempfilesAfterByteCap(t *testing.T) {
	if err := tempfiles.Cleanup(); err != nil {
		t.Fatal(err)
	}
	tmpdir := t.TempDir()
	t.Setenv("TMPDIR", tmpdir)
	capture, err := newCommandCapture(3)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.closeRemove()

	if _, err := capture.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if capture.path == "" || filepath.Dir(filepath.Dir(capture.path)) != tmpdir ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(capture.path)), "gax-") {
		t.Fatalf("spill path = %q, want a file in TMPDIR/gax-*", capture.path)
	}
	data, err := os.ReadFile(capture.path)
	if err != nil || string(data) != "1234" {
		t.Fatalf("spilled bytes = %q, err=%v", data, err)
	}
	capture.closeRemove()
	if _, err := os.Stat(capture.path); !os.IsNotExist(err) {
		t.Fatalf("unpreserved spill remains after close: %v", err)
	}
	if err := tempfiles.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCaptureSpillsWhenLineCapIsExceeded(t *testing.T) {
	if err := tempfiles.Cleanup(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	capture, err := newCommandCapture(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.closeRemove()

	output := strings.Repeat("x\n", 2001)
	if _, err := capture.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	if capture.path == "" {
		t.Fatal("output exceeding 2,000 lines was not spilled")
	}
	data, total, stored, _, _, _ := capture.snapshot(0)
	if string(data) != output || total != int64(len(output)) || stored != total {
		t.Fatalf("spilled capture length = %d, total=%d, stored=%d", len(data), total, stored)
	}
}

func TestCommandCaptureStopsForegroundProcessAtExactDrainLimit(t *testing.T) {
	capture, err := newCommandCapture()
	if err != nil {
		t.Fatal(err)
	}
	defer capture.closeRemove()
	capture.limit = 8
	stops := 0
	capture.setOverflowHandler(func() { stops++ })

	if _, err := capture.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("stop requests at exact limit = %d, want 1", stops)
	}
	data, total, stored, _, overflow, _ := capture.snapshot(0)
	if string(data) != "12345678" || total != 8 || stored != 8 || overflow {
		t.Fatalf("foreground capture at limit: data=%q total=%d stored=%d overflow=%v", data, total, stored, overflow)
	}
	if _, err := capture.Write([]byte("later")); err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("foreground stop requests after limit = %d, want one", stops)
	}
}

func TestCommandCaptureTaskDropsChunkThatReachesDrainLimit(t *testing.T) {
	capture, err := newCommandCapture()
	if err != nil {
		t.Fatal(err)
	}
	defer capture.closeRemove()
	capture.limit = 8
	stops := 0
	capture.setOverflowHandler(func() { stops++ })
	if _, err := capture.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	capture.setTaskMode()

	if _, err := capture.Write([]byte("5678")); err != nil {
		t.Fatal(err)
	}
	data, total, stored, _, overflow, _ := capture.snapshot(0)
	if string(data) != "1234" || total != 8 || stored != 4 || !overflow || stops != 1 {
		t.Fatalf("task capture at limit: data=%q total=%d stored=%d overflow=%v stops=%d", data, total, stored, overflow, stops)
	}
	if _, err := capture.Write([]byte("later")); err != nil {
		t.Fatal(err)
	}
	_, total, stored, _, overflow, _ = capture.snapshot(0)
	if total != 13 || stored != 4 || !overflow || stops != 1 {
		t.Fatalf("task capture after overflow: total=%d stored=%d overflow=%v stops=%d", total, stored, overflow, stops)
	}
}
