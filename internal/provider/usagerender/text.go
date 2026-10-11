// Package usagerender contains helpers shared by provider usage reports.
package usagerender

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"

	terminalocale "github.com/luank3/gax/internal/system/locale"
)

const (
	usageBarWidth  = 20
	usageBarColumn = 10
	fallbackWidth  = 100
)

// Window is the common display shape shared by provider quota-window reports.
type Window struct {
	Label       string
	UsedPercent float64
	ResetAt     time.Time
	Note        *string
}

// WriteHeading prints a plain-text usage heading with optional, sanitized details.
func WriteHeading(writer io.Writer, name string, details ...string) {
	fmt.Fprint(writer, StripLineControls(name))
	for _, detail := range details {
		if detail = StripLineControls(detail); detail != "" {
			fmt.Fprintf(writer, " · %s", detail)
		}
	}
	fmt.Fprintln(writer)
}

// WriteValue prints one aligned, sanitized value row.
func WriteValue(writer io.Writer, label, value string) {
	fmt.Fprintf(writer, "  %-7s %s\n", StripLineControls(label), StripLineControls(value))
}

// WriteWindow prints one quota bar and its reset time, wrapping the reset details when needed.
func WriteWindow(writer io.Writer, window Window, displayWidth int) {
	writeWindow(writer, window, displayWidth, terminalocale.HaveUTF8())
}

func writeWindow(writer io.Writer, window Window, displayWidth int, haveUTF8 bool) {
	if displayWidth <= 0 {
		displayWidth = fallbackWidth
	}
	used := math.Max(0, math.Min(100, window.UsedPercent))
	if math.IsNaN(used) {
		used = 0
	}
	filled := int(used/100*usageBarWidth + 0.5)
	fill, empty := "#", "-"
	if haveUTF8 {
		fill, empty = "█", "░"
	}
	bar := strings.Repeat(fill, filled) + strings.Repeat(empty, usageBarWidth-filled)
	label := StripLineControls(window.Label)
	percentText := fmt.Sprintf(" %3d%% used", int(used+0.5))
	resetTime := formatResetTime(window.ResetAt, time.Now())
	resetText := " · resets " + resetTime
	var note string
	if window.Note != nil {
		note = " · " + StripLineControls(*window.Note)
	}
	rowWidth := usageBarColumn + usageBarWidth + len(percentText) + len(resetText+note)
	if rowWidth <= displayWidth {
		fmt.Fprintf(writer, "  %-7s %s%s%s\n", label, bar, percentText, resetText+note)
		return
	}
	fmt.Fprintf(writer, "  %-7s %s%s\n", label, bar, percentText)
	fmt.Fprintf(writer, "%*sresets %s%s\n", usageBarColumn, "", resetTime, note)
}

func formatResetTime(resetAt, now time.Time) string {
	resetLocal := resetAt.Local()
	nowLocal := now.Local()
	if resetLocal.Year() == nowLocal.Year() && resetLocal.YearDay() == nowLocal.YearDay() {
		return resetLocal.Format("15:04")
	}
	return resetLocal.Format("Mon Jan 02, 15:04")
}

// StripLineControls removes terminal control sequences and flattens line breaks for one-line
// usage labels and notes returned by providers.
func StripLineControls(text string) string {
	const (
		textState = iota
		escapeState
		csiState
		oscState
		stringState
		intermediateState
		oscEscapeState
		stringEscapeState
	)
	state := textState
	var out strings.Builder
	out.Grow(len(text))
	for index := 0; index < len(text); index++ {
		value := text[index]
	reprocess:
		switch state {
		case textState:
			if value == 0x1b {
				state = escapeState
			} else if value == '\n' || value == '\t' {
				out.WriteByte(' ')
			} else if value >= 0x20 && value != 0x7f {
				out.WriteByte(value)
			}
		case escapeState:
			switch value {
			case '[':
				state = csiState
			case ']':
				state = oscState
			case 'P', '^', '_':
				state = stringState
			default:
				if value >= 0x20 && value <= 0x2f {
					state = intermediateState
				} else if value >= 0x30 && value <= 0x7e {
					state = textState
				} else {
					state = textState
					goto reprocess
				}
			}
		case csiState:
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			if value >= 0x40 && value <= 0x7e {
				state = textState
			}
		case intermediateState:
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			if value >= 0x30 && value <= 0x7e {
				state = textState
			}
		case oscState:
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			if value == 0x07 {
				state = textState
			} else if value == 0x1b {
				state = oscEscapeState
			}
		case stringState:
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			if value == 0x1b {
				state = stringEscapeState
			}
		case oscEscapeState:
			if value == '\\' {
				state = textState
				goto nextByte
			}
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			state = oscState
			goto reprocess
		case stringEscapeState:
			if value == '\\' {
				state = textState
				goto nextByte
			}
			if value == '\n' || value == 0x18 || value == 0x1a {
				state = textState
				goto reprocess
			}
			state = stringState
			goto reprocess
		}
	nextByte:
	}
	return strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return -1
		}
		return value
	}, out.String())
}
