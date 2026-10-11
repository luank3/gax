package paste

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCaptureClipboardTextNormalizesAndConvertsDroppedFileURI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("clipboard helpers are native executables on Windows")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "pbpaste")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'file:///tmp/a%%20b.PNG\\r\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	got, ok := CaptureClipboardText(context.Background())
	if !ok || got != "[pasted image: /tmp/a b.PNG] " {
		t.Fatalf("CaptureClipboardText() = %q, %t; want normalized image path", got, ok)
	}
}

func TestCaptureClipboardTextReturnsFalseWhenNoHelperResponds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("clipboard helpers are native executables on Windows")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "")
	if got, ok := CaptureClipboardText(context.Background()); got != "" || ok {
		t.Fatalf("CaptureClipboardText() = %q, %t; want no clipboard value", got, ok)
	}
}

func TestCopyClipboardUsesNativeHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("clipboard helpers are native executables on Windows")
	}
	dir := t.TempDir()
	output := filepath.Join(t.TempDir(), "clipboard.txt")
	helper := filepath.Join(dir, "pbcopy")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n/bin/cat > \"$GAX_TEST_CLIPBOARD\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GAX_TEST_CLIPBOARD", output)
	t.Setenv("WAYLAND_DISPLAY", "")
	unsetEnvForClipboardTest(t, "SSH_TTY")
	unsetEnvForClipboardTest(t, "SSH_CONNECTION")
	if err := CopyClipboard(context.Background(), "latest answer", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "latest answer" {
		t.Fatalf("native clipboard received %q, err=%v", got, err)
	}
}

func unsetEnvForClipboardTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestClipboardOSC52SequenceEncodesPlainAndTmuxForms(t *testing.T) {
	plain, err := ClipboardOSC52Sequence("answer", false)
	if err != nil || string(plain) != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("answer"))+"\x07" {
		t.Fatalf("plain OSC 52 sequence = %q, err=%v", plain, err)
	}
	tmux, err := ClipboardOSC52Sequence("answer", true)
	want := "\x1bPtmux;\x1b\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("answer")) + "\x07\x1b\\"
	if err != nil || string(tmux) != want {
		t.Fatalf("tmux OSC 52 sequence = %q, err=%v; want %q", tmux, err, want)
	}
	if _, err := ClipboardOSC52Sequence(string(make([]byte, 100001)), false); err == nil {
		t.Fatal("oversized OSC 52 payload succeeded")
	}
}
