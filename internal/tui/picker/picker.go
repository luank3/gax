// Package picker implements an interactive filterable list picker.
package picker

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luank3/gax/internal/platform"
)

// Item is one selectable entry in a picker.
type Item struct {
	ID    string
	Label string
}

// Run displays an interactive filterable picker and returns the selected item (or nil on cancel).
func Run(title string, items []Item, in *os.File, out io.Writer) (*Item, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if !platform.IsTerminal(in) {
		return &items[0], nil
	}
	st, err := platform.EnterRaw(in, nil, false)
	if err != nil {
		return nil, err
	}
	defer st.Restore()

	query := ""
	idx := 0
	filter := func() []Item {
		if query == "" {
			return items
		}
		low := strings.ToLower(query)
		var matched []Item
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Label+" "+it.ID), low) {
				matched = append(matched, it)
			}
		}
		return matched
	}

	render := func(matched []Item) {
		_, _ = fmt.Fprintf(out, "\r\x1b[2K%s > %s", title, query)
		if len(matched) > 0 && idx < len(matched) {
			_, _ = fmt.Fprintf(out, "  [%s]", matched[idx].Label)
		}
	}

	reader := bufio.NewReader(in)
	for {
		matched := filter()
		if idx >= len(matched) {
			idx = max(0, len(matched)-1)
		}
		render(matched)

		rn, _, err := reader.ReadRune()
		if err != nil {
			_, _ = io.WriteString(out, "\r\n")
			return nil, err
		}
		switch rn {
		case '\r', '\n':
			if rn == '\n' && reader.Buffered() == 0 {
				// Ctrl-J in raw mode arrives as '\n' (0x0a)
				if len(matched) > 0 {
					idx = (idx + 1) % len(matched)
				}
				continue
			}
			_, _ = io.WriteString(out, "\r\n")
			if len(matched) == 0 {
				return nil, nil
			}
			chosen := matched[idx]
			return &chosen, nil
		case 0x1b:
			if reader.Buffered() > 0 {
				b2, _ := reader.ReadByte()
				if b2 == '[' {
					b3, _ := reader.ReadByte()
					switch b3 {
					case 'A':
						if len(matched) > 0 {
							idx = (idx - 1 + len(matched)) % len(matched)
						}
						continue
					case 'B':
						if len(matched) > 0 {
							idx = (idx + 1) % len(matched)
						}
						continue
					}
				}
			}
			_, _ = io.WriteString(out, "\r\n")
			return nil, nil
		case 0x03, 0x04: // Ctrl-C, Ctrl-D
			_, _ = io.WriteString(out, "\r\n")
			return nil, nil
		case 0x0b: // Ctrl-K (prev)
			if len(matched) > 0 {
				idx = (idx - 1 + len(matched)) % len(matched)
			}
		case 0x17: // Ctrl-W
			query = ""
		case 0x7f, 0x08: // Backspace
			if rs := []rune(query); len(rs) > 0 {
				query = string(rs[:len(rs)-1])
			}
		default:
			if rn >= 32 {
				query += string(rn)
			}
		}
	}
}
