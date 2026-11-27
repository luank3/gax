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

		b, err := reader.ReadByte()
		if err != nil {
			_, _ = io.WriteString(out, "\r\n")
			return nil, err
		}
		switch b {
		case '\r':
			_, _ = io.WriteString(out, "\r\n")
			if len(matched) == 0 {
				return nil, nil
			}
			chosen := matched[idx]
			return &chosen, nil
		case 0x1b, 0x03, 0x04: // Esc, Ctrl-C, Ctrl-D
			_, _ = io.WriteString(out, "\r\n")
			return nil, nil
		case 0x0a: // Ctrl-J (next)
			if len(matched) > 0 {
				idx = (idx + 1) % len(matched)
			}
		case 0x0b: // Ctrl-K (prev)
			if len(matched) > 0 {
				idx = (idx - 1 + len(matched)) % len(matched)
			}
		case 0x17: // Ctrl-W
			query = ""
		case 0x7f, 0x08: // Backspace
			if len(query) > 0 {
				query = query[:len(query)-1]
			}
		default:
			if b >= 32 && b < 127 {
				query += string(b)
			}
		}
	}
}
