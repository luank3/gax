//go:build darwin

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func readClipboard(ctx context.Context) (ClipboardData, error) {
	tmpDir, err := os.MkdirTemp("", "gax-clip-*")
	if err == nil {
		defer os.RemoveAll(tmpDir)
		imgPath := filepath.Join(tmpDir, "clip.png")
		script := `try
set d to the clipboard as «class PNGf»
set f to open for access POSIX file "` + imgPath + `" with write permission
write d to f
close access f
return "ok"
on error
return ""
end try`
		if out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output(); err == nil && strings.TrimSpace(string(out)) == "ok" {
			if pngBytes, err := os.ReadFile(imgPath); err == nil && len(pngBytes) > 0 {
				return ClipboardData{ImagePNG: pngBytes, MediaType: "image/png"}, nil
			}
		}
	}
	out, err := exec.CommandContext(ctx, "pbpaste").Output()
	if err != nil {
		return ClipboardData{}, err
	}
	return ClipboardData{Text: string(out)}, nil
}

func writeClipboard(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
