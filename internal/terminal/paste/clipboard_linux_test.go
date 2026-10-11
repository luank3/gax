//go:build linux

package paste

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureClipboardPrefersCompleteImageAndCleansItUp(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "clipboard.png")
	image := framedPNG()
	if err := os.WriteFile(imagePath, image, 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "wl-paste")
	script := "#!/bin/sh\ncase \"$1\" in\n  --list-types) printf 'text/plain\\nimage/png\\n' ;;\n  -t) cat \"$CLIPBOARD_IMAGE\" ;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	textHelper := filepath.Join(dir, "pbpaste")
	if err := os.WriteFile(textHelper, []byte("#!/bin/sh\nprintf 'fallback text'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("CLIPBOARD_IMAGE", imagePath)
	t.Setenv("TMPDIR", dir)

	got, ok := CaptureClipboard(context.Background())
	if !ok || !strings.HasPrefix(got, "[pasted image: ") || !strings.HasSuffix(got, "] ") {
		t.Fatalf("CaptureClipboard() = %q, %t; want image marker", got, ok)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(got, "[pasted image: "), "] ")
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(image) {
		t.Fatalf("stored image = %d bytes, err=%v; want original image", len(stored), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored image mode = %v, err=%v; want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("temporary directory mode = %v, err=%v; want 0700", info, err)
	}
	if err := Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pasted image after Cleanup() error = %v, want not-exist", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory after Cleanup() error = %v, want not-exist", err)
	}
}

func TestCaptureClipboardFallsBackToTextForIncompleteImage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "clipboard.png")
	if err := os.WriteFile(imagePath, []byte("not a complete png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wl-paste"), []byte("#!/bin/sh\ncase \"$1\" in\n  --list-types) printf 'image/png\\ntext/plain\\n' ;;\n  -t) cat \"$CLIPBOARD_IMAGE\" ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pbpaste"), []byte("#!/bin/sh\nprintf 'plain text\\r\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("CLIPBOARD_IMAGE", imagePath)

	got, ok := CaptureClipboard(context.Background())
	if !ok || got != "plain text\n" {
		t.Fatalf("CaptureClipboard() = %q, %t; want normalized text fallback", got, ok)
	}
}

func TestReadClipboardImageUsesXclipWhenWaylandIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "clipboard.png")
	image := framedPNG()
	if err := os.WriteFile(imagePath, image, 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "xclip")
	script := "#!/bin/sh\ncase \"$*\" in\n  *TARGETS*) printf 'TARGETS\\nimage/png\\n' ;;\n  *image/png*) /bin/cat \"$CLIPBOARD_IMAGE\" ;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("CLIPBOARD_IMAGE", imagePath)

	ctx, cancel := clipboardContext(context.Background())
	defer cancel()
	got, ok := readClipboardImage(ctx)
	if !ok || string(got) != string(image) {
		t.Fatalf("readClipboardImage() = %d bytes, %t; want the X11 clipboard image", len(got), ok)
	}
}
