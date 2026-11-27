// Package width computes terminal display widths, word wrapping, and VT line settling.
package width

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// StringWidth returns the visible column width of s, skipping ANSI CSI/OSC sequences.
func StringWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i = skipEscape(s, i)
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		if r == '\t' {
			w += 4 - (w % 4)
			continue
		}
		w += RuneWidth(r)
	}
	return w
}

// RuneWidth returns the display width of a single rune (0, 1, or 2).
func RuneWidth(r rune) int {
	if r < 32 || r == 0x7f || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if isWide(r) {
		return 2
	}
	return 1
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0x1f300 && r <= 0x1f9ff)
}

func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j < len(s) {
			return j + 1
		}
		return len(s)
	case ']':
		j := i + 2
		for j < len(s) {
			if s[j] == '\a' {
				return j + 1
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return len(s)
	default:
		return i + 2
	}
}

// Wrap wraps text to maxCols preserving words where possible.
func Wrap(text string, maxCols int) string {
	if maxCols < 10 {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		if StringWidth(line) <= maxCols {
			out = append(out, line)
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := words[0]
		curW := StringWidth(cur)
		for _, w := range words[1:] {
			ww := StringWidth(w)
			if curW+1+ww <= maxCols {
				cur += " " + w
				curW += 1 + ww
			} else {
				out = append(out, cur)
				cur = w
				curW = ww
			}
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}

// ResolveVT settles simple cursor-up (\x1b[nA) and erase-line (\x1b[K / \x1b[2K)
// sequences for plain pager output (Ctrl-O).
func ResolveVT(raw string) string {
	var lines []string
	cur := 0
	ensure := func(idx int) {
		for len(lines) <= idx {
			lines = append(lines, "")
		}
	}
	for i := 0; i < len(raw); {
		if raw[i] == '\n' {
			cur++
			ensure(cur)
			i++
			continue
		}
		if raw[i] == '\r' {
			ensure(cur)
			lines[cur] = ""
			i++
			continue
		}
		if raw[i] == '\x1b' && i+1 < len(raw) && raw[i+1] == '[' {
			j := i + 2
			for j < len(raw) && (raw[j] < 0x40 || raw[j] > 0x7e) {
				j++
			}
			if j < len(raw) {
				cmd := raw[j]
				param := raw[i+2 : j]
				switch cmd {
				case 'A':
					n := 1
					if param != "" {
						var parsed int
						for _, ch := range param {
							if ch >= '0' && ch <= '9' {
								parsed = parsed*10 + int(ch-'0')
							}
						}
						if parsed > 0 {
							n = parsed
						}
					}
					cur = max(0, cur-n)
					i = j + 1
					continue
				case 'K':
					ensure(cur)
					lines[cur] = ""
					i = j + 1
					continue
				}
			}
		}
		r, sz := utf8.DecodeRuneInString(raw[i:])
		ensure(cur)
		lines[cur] += string(r)
		i += sz
	}
	return strings.Join(lines, "\n")
}
