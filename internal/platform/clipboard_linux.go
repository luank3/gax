//go:build linux

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func readClipboard(ctx context.Context) (ClipboardData, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if p, err := exec.LookPath("wl-paste"); err == nil {
			if out, err := exec.CommandContext(ctx, p, "--type", "image/png").Output(); err == nil && len(out) > 0 {
				return ClipboardData{ImagePNG: out, MediaType: "image/png"}, nil
			}
			out, err := exec.CommandContext(ctx, p, "--no-newline").Output()
			if err == nil {
				return ClipboardData{Text: string(out)}, nil
			}
		}
	}
	if p, err := exec.LookPath("xclip"); err == nil {
		if out, err := exec.CommandContext(ctx, p, "-selection", "clipboard", "-t", "image/png", "-o").Output(); err == nil && len(out) > 0 {
			return ClipboardData{ImagePNG: out, MediaType: "image/png"}, nil
		}
		out, err := exec.CommandContext(ctx, p, "-selection", "clipboard", "-o").Output()
		if err == nil {
			return ClipboardData{Text: string(out)}, nil
		}
	}
	return ClipboardData{}, fmt.Errorf("clipboard unavailable (install wl-paste or xclip)")
}

func writeClipboard(ctx context.Context, text string) error {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if p, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.CommandContext(ctx, p)
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	}
	if p, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.CommandContext(ctx, p, "-selection", "clipboard")
		cmd.Stdin = bytes.NewBufferString(text)
		return cmd.Run()
	}
	return fmt.Errorf("clipboard unavailable (install wl-copy or xclip)")
}
