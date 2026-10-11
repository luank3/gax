// Package banner renders the interactive identity and key-tip rows.
package banner

import (
	"io"
	"strings"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	terminalwidth "github.com/luank3/gax/internal/terminal/width"
)

const (
	ansiBold    = "\x1b[1m"
	ansiBoldOff = "\x1b[22m"
	ansiDim     = "\x1b[2m"
	ansiReset   = "\x1b[0m"
)

type Identity struct {
	Provider string
	Model    string
	Effort   string
	Preset   string
}

type segment struct {
	separator string
	text      string
	open      string
	close     string
}

type writer struct {
	output   strings.Builder
	columns  int
	column   int
	fresh    bool
	styled   bool
	styleOn  string
	styleOff string
}

// IdentityLine writes a wrapped identity row using the configured display width.
func IdentityLine(output io.Writer, identity Identity, columns int) error {
	var rendered strings.Builder
	view := writer{columns: columns, column: 2, fresh: true}
	view.openRow()

	segments := []segment{{text: "gax", open: ansiBold, close: ansiBoldOff}}
	if identity.Preset != "" {
		segments = append(segments, segment{
			separator: " ", text: "[" + identity.Preset + "]",
			open: terminaltheme.Open(terminaltheme.Stance), close: terminaltheme.Close(terminaltheme.Stance),
		})
	}
	if identity.Provider == "" {
		segments = append(segments, segment{
			separator: " ", text: "› no provider — use /provider", open: ansiDim, close: ansiBoldOff,
		})
	} else {
		segments = append(segments, segment{separator: " ", text: "› " + identity.Provider, open: ansiDim, close: ansiBoldOff})
		model := identity.Model
		if model == "" {
			model = "no model — use /model (or /provider)"
		} else if identity.Effort != "" {
			model += " · " + identity.Effort
		}
		segments = append(segments, segment{separator: " · ", text: model, open: ansiDim, close: ansiBoldOff})
	}
	for _, part := range segments {
		view.put(part)
	}
	view.closeRow()
	rendered.WriteString(view.output.String())
	_, err := io.WriteString(output, rendered.String())
	return err
}

// Print writes the identity and the interactive key tips.
func Print(output io.Writer, identity Identity, columns int) error {
	if err := IdentityLine(output, identity, columns); err != nil {
		return err
	}
	var rendered strings.Builder
	view := writer{columns: columns, column: 2, fresh: true}
	view.openRow()
	view.put(segment{text: "ctrl-d quit", open: ansiDim, close: ansiBoldOff})
	view.put(segment{separator: " · ", text: "try /help", open: ansiDim, close: ansiBoldOff})
	view.closeRow()
	rendered.WriteString(view.output.String())
	_, err := io.WriteString(output, rendered.String())
	return err
}

func (view *writer) openRow() {
	view.output.WriteString(terminaltheme.Open(terminaltheme.Chrome))
	view.output.WriteString("▌")
	view.output.WriteString(terminaltheme.Close(terminaltheme.Chrome))
	view.output.WriteByte(' ')
	view.column = 2
	view.fresh = true
}

func (view *writer) closeRow() {
	view.style(nil, nil)
	view.output.WriteByte('\n')
}

func (view *writer) rowBreak() {
	view.style(nil, nil)
	view.output.WriteByte('\n')
	view.openRow()
	view.output.WriteString("  ")
	view.column = 4
}

func (view *writer) style(open, close *string) {
	if open != nil && view.styled && view.styleOn == *open {
		return
	}
	if view.styled {
		view.output.WriteString(view.styleOff)
	}
	view.styled = open != nil
	if open != nil {
		view.styleOn, view.styleOff = *open, *close
		view.output.WriteString(*open)
	} else {
		view.styleOn, view.styleOff = "", ""
	}
}

func (view *writer) put(part segment) {
	if view.columns < 1 {
		view.columns = 100
	}
	if !view.fresh {
		if view.column+terminalwidth.Cells(part.separator)+terminalwidth.Cells(part.text) > view.columns {
			view.rowBreak()
		} else {
			view.style(&part.open, &part.close)
			view.output.WriteString(part.separator)
			view.column += terminalwidth.Cells(part.separator)
		}
	}
	view.style(&part.open, &part.close)
	text := part.text
	for terminalwidth.Cells(text) > view.columns-view.column {
		row, remaining := wrapRow(text, view.columns-view.column)
		view.output.WriteString(row)
		view.column += terminalwidth.Cells(row)
		text = remaining
		view.rowBreak()
		view.style(&part.open, &part.close)
	}
	view.output.WriteString(text)
	view.column += terminalwidth.Cells(text)
	view.fresh = false
}

func wrapRow(text string, budget int) (row, remaining string) {
	if budget < 1 {
		budget = 1
	}
	cells, lastSpace, fitBytes := 0, -1, 0
	for index := 0; index < len(text); {
		value, size := utf8.DecodeRuneInString(text[index:])
		cellWidth := terminalwidth.RuneCells(value)
		if cells+cellWidth > budget {
			if value == ' ' && cells == budget {
				lastSpace = index
			}
			if lastSpace >= 0 {
				end := lastSpace
				for end > 0 && text[end-1] == ' ' {
					end--
				}
				return text[:end], text[lastSpace+1:]
			}
			if fitBytes == 0 {
				fitBytes = index + size
			}
			return text[:fitBytes], text[fitBytes:]
		}
		if value == ' ' {
			lastSpace = index
		}
		cells += cellWidth
		index += size
		fitBytes = index
	}
	return text, ""
}
