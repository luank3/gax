package agent

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/luank3/gax/internal/core"
)

const maxPromptImageBytes = 20 << 20

// ExtractPromptImages resolves @path image mentions in prompt text into base64 attachments.
func ExtractPromptImages(cwd, text string) []core.ImageAttachment {
	if !strings.Contains(text, "@") {
		return nil
	}
	var out []core.ImageAttachment
	seen := map[string]bool{}
	for _, tok := range strings.Fields(text) {
		if !strings.HasPrefix(tok, "@") || len(tok) < 2 {
			continue
		}
		p := strings.TrimRight(tok[1:], ",.;)]}\"'")
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) && cwd != "" {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxPromptImageBytes {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		media := sniffImageMedia(raw)
		if media == "" {
			continue
		}
		seen[p] = true
		out = append(out, core.ImageAttachment{
			MediaType: media,
			Data:      base64.StdEncoding.EncodeToString(raw),
		})
	}
	return out
}

func sniffImageMedia(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "image/jpeg"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return "image/gif"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}
