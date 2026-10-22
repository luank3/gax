package paste

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/luank3/gax/internal/system/spawn"
)

const (
	clipboardTimeout  = 5 * time.Second
	maxClipboardText  = 1 << 20
	maxClipboardTypes = 64 << 10
	maxClipboardImage = 64 << 20
)

func CaptureClipboardText(ctx context.Context) (string, bool) {
	ctx, cancel := clipboardContext(ctx)
	defer cancel()
	return captureClipboardText(ctx)
}

// CaptureClipboard prefers a complete supported clipboard image, then falls back to text.
func CaptureClipboard(ctx context.Context) (string, bool) {
	ctx, cancel := clipboardContext(ctx)
	defer cancel()
	if image, ok := readClipboardImage(ctx); ok {
		if mime, complete := sniffClipboardImage(image); complete {
			if marker, persisted := persistClipboardImage(image, mime); persisted {
				return marker, true
			}
		}
	}
	return captureClipboardText(ctx)
}

func clipboardContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, clipboardTimeout)
}

func captureClipboardText(ctx context.Context) (string, bool) {
	text, ok := readClipboardText(ctx)
	if !ok {
		return "", false
	}
	normalized := NormalizeText(text)
	if normalized == "" {
		return "", false
	}
	if converted, ok := FileURIsToPaths(normalized); ok {
		return converted, true
	}
	return normalized, true
}

func readClipboardText(ctx context.Context) ([]byte, bool) {
	if text, ok := platformClipboardText(ctx); ok {
		return text, true
	}
	if text, ok := runClipboardCommand(ctx, maxClipboardText, "pbpaste"); ok {
		return text, true
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, ok := runClipboardCommandOutput(ctx, maxClipboardTypes, "wl-paste", "--list-types"); ok {
			if text, ok := runClipboardCommand(ctx, maxClipboardText, "wl-paste", "-n", "-t", "text"); ok {
				return text, true
			}
		}
	}
	if text, ok := runClipboardCommand(ctx, maxClipboardText, "xclip", "-selection", "clipboard", "-o"); ok {
		return text, true
	}
	if text, ok := runClipboardCommand(ctx, maxClipboardText, "xsel", "-b", "-o"); ok {
		return text, true
	}
	return nil, false
}

func readClipboardImage(ctx context.Context) ([]byte, bool) {
	if image, ok := platformClipboardImage(ctx); ok {
		return image, true
	}
	if runtime.GOOS == "darwin" {
		return readDarwinClipboardImage(ctx)
	}
	if !supportsX11ClipboardImage(runtime.GOOS) {
		return nil, false
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		listing, ok := runClipboardCommandOutput(ctx, maxClipboardTypes, "wl-paste", "--list-types")
		if ok {
			mime := pickImageMIME(string(listing))
			if mime == "" {
				return nil, false
			}
			return runClipboardCommand(ctx, maxClipboardImage, "wl-paste", "-t", mime)
		}
	}
	return readX11ClipboardImage(ctx)
}

func supportsX11ClipboardImage(goos string) bool {
	switch goos {
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	default:
		return false
	}
}

func readX11ClipboardImage(ctx context.Context) ([]byte, bool) {
	listing, ok := runClipboardCommandOutput(ctx, maxClipboardTypes,
		"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	if !ok {
		return nil, false
	}
	mime := pickImageMIME(string(listing))
	if mime == "" {
		return nil, false
	}
	return runClipboardCommand(ctx, maxClipboardImage,
		"xclip", "-selection", "clipboard", "-t", mime, "-o")
}

func readDarwinClipboardImage(ctx context.Context) ([]byte, bool) {
	file, err := os.CreateTemp("", "gax-clip-*")
	if err != nil {
		return nil, false
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, false
	}
	defer os.Remove(path)
	escapedPath := strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(path)
	script := fmt.Sprintf("set f to open for access POSIX file \"%s\" with write permission\nwrite (the clipboard as «class PNGf») to f\nclose access f", escapedPath)
	_, _ = runClipboardCommandOutput(ctx, maxClipboardTypes, "osascript", "-e", script)
	clipboardImage, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer clipboardImage.Close()
	data, err := io.ReadAll(io.LimitReader(clipboardImage, maxClipboardImage+1))
	if err != nil || len(data) == 0 || len(data) > maxClipboardImage {
		return nil, false
	}
	return data, true
}

func pickImageMIME(types string) string {
	for _, candidate := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		for _, offered := range strings.Split(types, "\n") {
			if offered == candidate {
				return candidate
			}
		}
	}
	return ""
}

func runClipboardCommand(ctx context.Context, maxBytes int, name string, args ...string) ([]byte, bool) {
	output, ok := runClipboardCommandOutput(ctx, maxBytes, name, args...)
	return output, ok && len(output) != 0
}

func runClipboardCommandOutput(ctx context.Context, maxBytes int, name string, args ...string) ([]byte, bool) {
	argv := append([]string{name}, args...)
	output, err := spawn.CaptureStdoutContext(ctx, argv, maxBytes)
	return output, err == nil
}

func persistClipboardImage(data []byte, mime string) (string, bool) {
	extension := map[string]string{
		"image/png":  ".png",
		"image/jpeg": ".jpg",
		"image/gif":  ".gif",
		"image/webp": ".webp",
	}[mime]
	if extension == "" {
		return "", false
	}
	path, ok := createTrackedClipboardImage(data, extension)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("[pasted image: %s] ", filepath.Clean(path)), true
}
