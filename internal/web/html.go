package web

import (
	"html"
	"strings"
	"unicode"
)

// HTMLToText converts an HTML document to readable plain text, stripping
// script/style/noscript blocks and normalizing whitespace.
func HTMLToText(raw string) string {
	var out strings.Builder
	var skipTag string
	i := 0
	for i < len(raw) {
		if raw[i] == '<' {
			end := strings.IndexByte(raw[i:], '>')
			if end < 0 {
				break
			}
			tag := strings.TrimSpace(raw[i+1 : i+end])
			low := strings.ToLower(tag)
			name := low
			if sp := strings.IndexAny(low, " \t\r\n/>"); sp >= 0 {
				name = low[:sp]
			}
			if skipTag != "" {
				if strings.HasPrefix(low, "/"+skipTag) {
					skipTag = ""
				}
				i += end + 1
				continue
			}
			switch name {
			case "script", "style", "noscript", "svg":
				if !strings.HasSuffix(tag, "/") {
					skipTag = name
				}
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "hr":
				out.WriteByte('\n')
			}
			i += end + 1
			continue
		}
		if skipTag != "" {
			i++
			continue
		}
		next := strings.IndexByte(raw[i:], '<')
		var chunk string
		if next < 0 {
			chunk = raw[i:]
			i = len(raw)
		} else {
			chunk = raw[i : i+next]
			i += next
		}
		out.WriteString(html.UnescapeString(chunk))
	}

	lines := strings.Split(out.String(), "\n")
	var cleaned []string
	blank := 0
	for _, l := range lines {
		norm := collapseSpaces(l)
		if norm == "" {
			blank++
			if blank <= 1 && len(cleaned) > 0 {
				cleaned = append(cleaned, "")
			}
			continue
		}
		blank = 0
		cleaned = append(cleaned, norm)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func collapseSpaces(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			inSpace = true
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
