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
	codeFence := ""
	codeLang := ""

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if !inCode && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			inCode = true
			codeFence = trimmed[:3]
			codeLang = strings.TrimSpace(trimmed[3:])
			out = append(out, pal.Dim+"─── "+codeLang+" ───"+pal.Reset)
			continue
		}
		if inCode && strings.HasPrefix(trimmed, codeFence) && strings.TrimSpace(strings.TrimPrefix(trimmed, codeFence)) == "" {
			inCode = false
			codeFence = ""
			codeLang = ""
			out = append(out, pal.Dim+"───────────"+pal.Reset)
			continue
		}
		if inCode {
			if codeLang == "diff" || codeLang == "patch" {
				if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
					out = append(out, pal.DiffAdd+line+pal.Reset)
					continue
				}
				if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
					out = append(out, pal.DiffDel+line+pal.Reset)
					continue
				}
			}
			out = append(out, pal.Dim+"  "+line+pal.Reset)
			continue
		}
		if isHorizontalRule(trimmed) {
			w := cols
			if w <= 0 || w > 40 {
				w = 40
			}
			out = append(out, pal.Dim+strings.Repeat("─", w)+pal.Reset)
			continue
		}
		if strings.Contains(trimmed, "|") && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			end := i + 2
			for end < len(lines) && strings.Contains(lines[end], "|") && strings.TrimSpace(lines[end]) != "" {
				end++
			}
			out = append(out, renderTable(lines[i:end], cols, pal)...)
			i = end - 1
			continue
		}
		if strings.HasPrefix(trimmed, "#") && (len(trimmed) == 1 || strings.ContainsAny(trimmed[:min(7, len(trimmed))], " \t#")) {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if heading != "" {
				out = append(out, pal.Bold+pal.Accent+heading+pal.Reset)
				continue
			}
		}
		if strings.HasPrefix(trimmed, ">") {
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out = append(out, pal.Dim+"│ "+pal.Reset+width.Wrap(styleInline(quote, pal), max(10, cols-2)))
			continue
		}
		if indent, marker, rest, ok := parseListItem(line); ok {
			prefix := indent + marker + " "
			if pal.Reset != "" && marker != "•" {
				prefix = indent + pal.Accent + marker + pal.Reset + " "
			}
			out = append(out, prefix+width.Wrap(styleInline(rest, pal), max(10, cols-width.StringWidth(indent)-len(marker)-1)))
			continue
		}
		wrapped := width.Wrap(styleInline(line, pal), cols)
		out = append(out, wrapped)
	}
	return strings.Join(out, "\n")
}

func isHorizontalRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	ch := trimmed[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == ch {
			count++
		} else if trimmed[i] != ' ' && trimmed[i] != '\t' {
			return false
		}
	}
	return count >= 3
}

func parseListItem(line string) (indent, marker, rest string, ok bool) {
	spaces := 0
	for spaces < len(line) && (line[spaces] == ' ' || line[spaces] == '\t') {
		spaces++
	}
	t := line[spaces:]
	if len(t) >= 2 && (t[0] == '-' || t[0] == '*' || t[0] == '+') && t[1] == ' ' {
		return strings.Repeat(" ", spaces+2), "•", t[2:], true
	}
	dot := strings.IndexByte(t, '.')
	if dot > 0 && dot <= 3 && dot+1 < len(t) && t[dot+1] == ' ' {
		for i := 0; i < dot; i++ {
			if t[i] < '0' || t[i] > '9' {
				return "", "", "", false
			}
		}
		return strings.Repeat(" ", spaces+2), t[:dot+1], t[dot+2:], true
	}
	return "", "", "", false
}

func isTableSeparator(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		c = strings.TrimPrefix(c, ":")
		c = strings.TrimSuffix(c, ":")
		if len(c) < 3 || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

func splitTableRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	if t == "" {
		return nil
	}
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func renderTable(rows []string, cols int, pal theme.Palette) []string {
	if len(rows) < 2 {
		return rows
	}
	header := splitTableRow(rows[0])
	numCols := len(header)
	var body [][]string
	for _, r := range rows[2:] {
		cells := splitTableRow(r)
		if len(cells) > numCols {
			numCols = len(cells)
		}
		body = append(body, cells)
	}
	widths := make([]int, numCols)
	for c := 0; c < len(header); c++ {
		widths[c] = max(widths[c], width.StringWidth(header[c]))
	}
	for _, r := range body {
		for c := 0; c < len(r); c++ {
			widths[c] = max(widths[c], width.StringWidth(r[c]))
		}
	}
	// Shrink widest columns gently if total exceeds cols.
	if cols > 10 && numCols > 0 {
		sepW := (numCols - 1) * 3
		for {
			sum := sepW
			maxIdx := 0
			for i, w := range widths {
				sum += w
				if w > widths[maxIdx] {
					maxIdx = i
				}
			}
			if sum <= cols || widths[maxIdx] <= 6 {
				break
			}
			widths[maxIdx]--
		}
	}
	formatRow := func(cells []string, bold bool) string {
		var b strings.Builder
		for c := 0; c < numCols; c++ {
			if c > 0 {
				b.WriteString(pal.Dim + " │ " + pal.Reset)
			}
			val := ""
			if c < len(cells) {
				val = cells[c]
			}
			styled := styleInline(val, pal)
			if bold {
				styled = pal.Bold + val + pal.Reset
			}
			pad := widths[c] - width.StringWidth(val)
			b.WriteString(styled)
			if pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		return b.String()
	}
	var out []string
	out = append(out, formatRow(header, true))
	var rule strings.Builder
	for c := 0; c < numCols; c++ {
		if c > 0 {
			rule.WriteString("─┼─")
		}
		rule.WriteString(strings.Repeat("─", max(1, widths[c])))
	}
	out = append(out, pal.Dim+rule.String()+pal.Reset)
	for _, r := range body {
		out = append(out, formatRow(r, false))
	}
	return out
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
		if s[i] == '[' {
			if closeBr := strings.IndexByte(s[i+1:], ']'); closeBr >= 0 {
				after := i + 1 + closeBr + 1
				if after < len(s) && s[after] == '(' {
					if closePar := strings.IndexByte(s[after+1:], ')'); closePar >= 0 {
						label := s[i+1 : i+1+closeBr]
						linkURL := s[after+1 : after+1+closePar]
						b.WriteString(pal.Accent)
						b.WriteString(label)
						b.WriteString(pal.Reset)
						if linkURL != "" && linkURL != label {
							b.WriteString(pal.Dim + " (" + linkURL + ")" + pal.Reset)
						}
						i = after + 1 + closePar + 1
						continue
					}
				}
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
		if s[i] == '*' && (i+1 >= len(s) || s[i+1] != '*') {
			if end := strings.IndexByte(s[i+1:], '*'); end > 0 {
				b.WriteString("\x1b[3m")
				b.WriteString(s[i+1 : i+1+end])
				b.WriteString("\x1b[23m")
				i += end + 2
				continue
			}
		}
		if i+1 < len(s) && s[i] == '~' && s[i+1] == '~' {
			if end := strings.Index(s[i+2:], "~~"); end > 0 {
				b.WriteString("\x1b[9m")
				b.WriteString(s[i+2 : i+2+end])
				b.WriteString("\x1b[29m")
				i += end + 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
