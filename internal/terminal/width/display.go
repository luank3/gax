// Package width implements hax's terminal content-width rules.
package width

import "strings"

const (
	minimumWidth       = 20
	automaticWidthCap  = 100
	automaticTolerance = 10
	fallbackWidth      = 100
)

// AutoDisplayWidth caps widths beyond 110 columns at 100 and floors narrow widths at 20.
func AutoDisplayWidth(terminalWidth int) int {
	width := terminalWidth
	if width > automaticWidthCap+automaticTolerance {
		width = automaticWidthCap
	}
	if width < minimumWidth {
		width = minimumWidth
	}
	return width
}

// DisplayWidth applies an exact configured width or the auto/terminal mode to a terminal width.
func DisplayWidth(terminalWidth int, mode string, configuredWidth int) int {
	if configuredWidth >= minimumWidth {
		return configuredWidth
	}
	if terminalWidth <= 0 {
		terminalWidth = fallbackWidth
	}
	if !strings.EqualFold(mode, "terminal") {
		return AutoDisplayWidth(terminalWidth)
	}
	if terminalWidth < minimumWidth {
		return minimumWidth
	}
	return terminalWidth
}

// ContentColumns clamps DisplayWidth to the physical terminal when its width is known.
func ContentColumns(terminalWidth int, mode string, configuredWidth int) int {
	columns := DisplayWidth(terminalWidth, mode, configuredWidth)
	if terminalWidth > 0 && columns > terminalWidth {
		columns = terminalWidth
	}
	if columns <= 0 {
		return 1
	}
	return columns
}
