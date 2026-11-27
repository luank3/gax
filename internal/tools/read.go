package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"github.com/luank3/gax/internal/core"
	"github.com/luank3/gax/internal/skill"
)

const (
	maxImageBytes = 5 * 1024 * 1024
	maxImageDim   = 4096
)

type readTool struct{ reg *Registry }

func (t *readTool) Definition() core.ToolDef {
	return core.ToolDef{
		Name:        "read",
		Description: "Read a text file (with optional 1-indexed line offset and limit) or an image file.",
		Params: []core.ToolParam{
			{Name: "path", Type: "string", Required: true, Description: "Path to the file."},
			{Name: "offset", Type: "integer", Minimum: intPtr(1), Description: "1-indexed first line to return. Default 1."},
			{Name: "limit", Type: "integer", Minimum: intPtr(1), Description: "Maximum number of lines to return. Default: to EOF."},
		},
	}
}

func (t *readTool) Run(_ context.Context, arguments string, _ OutputSink) Result {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(arguments, &args); err != nil {
		return Result{Text: fmt.Sprintf("error: invalid arguments: %v", err), IsError: true}
	}
	if strings.TrimSpace(args.Path) == "" {
		return Result{Text: "error: 'path' is required", IsError: true}
	}

	if strings.HasPrefix(args.Path, "gax:skills/") {
		return t.readBuiltinSkill(args.Path, args.Offset, args.Limit)
	}

	resolved := t.reg.resolvePath(args.Path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return Result{Text: fmt.Sprintf("error reading %s: %v", args.Path, err), IsError: true}
	}

	if media := sniffImage(raw); media != "" {
		if len(raw) > maxImageBytes {
			return Result{
				Text:    fmt.Sprintf("error: image %s is %d bytes (limit %d); downscale it before reading", args.Path, len(raw), maxImageBytes),
				IsError: true,
			}
		}
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
			if cfg.Width > maxImageDim || cfg.Height > maxImageDim {
				return Result{
					Text:    fmt.Sprintf("error: image %s is %dx%d (limit %dpx per side); downscale it before reading", args.Path, cfg.Width, cfg.Height, maxImageDim),
					IsError: true,
				}
			}
		}
		return Result{
			Text: fmt.Sprintf("read image %s (%s, %d bytes)", args.Path, media, len(raw)),
			Images: []core.ImageAttachment{{
				MediaType: media,
				Data:      base64.StdEncoding.EncodeToString(raw),
			}},
		}
	}

	if bytes.IndexByte(raw[:min(8192, len(raw))], 0) >= 0 {
		return Result{Text: fmt.Sprintf("error: %s appears to be a binary file (%d bytes)", args.Path, len(raw)), IsError: true}
	}

	return Result{Text: sliceLines(string(raw), args.Offset, args.Limit, t.reg.outputCap())}
}

func (t *readTool) readBuiltinSkill(path string, offset, limit int) Result {
	for _, entry := range skill.Discover(skill.Options{}) {
		if entry.Path == path {
			body, err := entry.Load()
			if err != nil {
				return Result{Text: fmt.Sprintf("error reading %s: %v", path, err), IsError: true}
			}
			return Result{Text: sliceLines(body, offset, limit, t.reg.outputCap())}
		}
	}
	return Result{Text: fmt.Sprintf("error: built-in skill %q not found", path), IsError: true}
}

func sliceLines(content string, offset, limit, capBytes int) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := 0
	if offset > 1 {
		start = offset - 1
	}
	if start >= len(lines) {
		return fmt.Sprintf("(file has %d lines; offset %d is past EOF)", len(lines), offset)
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "... (%d more lines)\n", len(lines)-end)
	}
	return capOutput(b.String(), capBytes, "gax-read")
}

func sniffImage(raw []byte) string {
	if len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	if len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff {
		return "image/jpeg"
	}
	if len(raw) >= 6 && (bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a"))) {
		return "image/gif"
	}
	if len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	return ""
}
