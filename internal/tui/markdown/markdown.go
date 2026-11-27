// Package markdown renders streaming and static Markdown for terminal output.
package markdown

import (
	"strings"

	"github.com/luank3/gax/internal/tui/theme"
	"github.com/luank3/gax/internal/tui/width"
)

// Render formats src as ANSI-styled terminal text wrapped to cols.
func Render(src string, cols int, pal theme.Palette, enabled bool) string {
	if !enabled {
		return width.Wrap(src, cols)
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	inCode := false
	codeLang := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			if inCode {
				codeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				out = append(out, pal.Dim+"─── "+codeLang+" ───"+pal.Reset)
			} else {
				codeLang = ""
				out = append(out, pal.Dim+"───────────"+pal.Reset)
			}
			continue
		}
		if inCode {
			if codeLang == "diff" || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				if strings.HasPrefix(line, "+") {
					out = append(out, pal.DiffAdd+line+pal.Reset)
					continue
				}
				if strings.HasPrefix(line, "-") {
					out = append(out, pal.DiffDel+line+pal.Reset)
					continue
				}
			}
			out = append(out, pal.Dim+"  "+line+pal.Reset)
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out = append(out, pal.Bold+pal.Accent+heading+pal.Reset)
			continue
		}
		wrapped := width.Wrap(styleInline(line, pal), cols)
		out = append(out, wrapped)
	}
	return strings.Join(out, "\n")
}

func styleInline(s string, pal theme.Palette) string {
	if pal.Reset == "" {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				b.WriteString(pal.Accent)
				b.WriteString(s[i+1 : i+1+end])
				b.WriteString(pal.Reset)
				i += end + 2
				continue
			}
		}
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			if end := strings.Index(s[i+2:], "**"); end >= 0 {
				b.WriteString(pal.Bold)
				b.WriteString(s[i+2 : i+2+end])
				b.WriteString(pal.Reset)
				i += end + 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
