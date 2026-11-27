// Package live manages the in-place live-updating region below terminal scrollback
// (streaming preview, persistent spinner/elapsed/status footer with hysteresis).
package live

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/luank3/gax/internal/tui/width"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Region repaints a bounded number of terminal rows in place without disturbing scrollback.
type Region struct {
	mu       sync.Mutex
	w        io.Writer
	isTTY    bool
	cols     int
	maxRows  int
	lastRows int
}

// NewRegion returns a live-region writer over w.
func NewRegion(w io.Writer, isTTY bool) *Region {
	return &Region{w: w, isTTY: isTTY, cols: 80, maxRows: 20}
}

// SetCols configures the terminal column width used to count wrapped rows.
func (r *Region) SetCols(cols int) {
	if r == nil || cols <= 0 {
		return
	}
	r.mu.Lock()
	r.cols = cols
	r.mu.Unlock()
}

// SetMaxRows configures the maximum number of terminal rows Paint may occupy.
func (r *Region) SetMaxRows(maxRows int) {
	if r == nil || maxRows <= 0 {
		return
	}
	r.mu.Lock()
	r.maxRows = maxRows
	r.mu.Unlock()
}

// Paint replaces the currently painted live region with lines, accounting for wrapping,
// capping height to maxRows, and using DEC synchronized output to prevent flicker.
func (r *Region) Paint(lines ...string) {
	if r == nil || r.w == nil || !r.isTTY {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	if r.maxRows > 0 && len(flat) > 1 {
		used := 0
		start := len(flat)
		for i := len(flat) - 1; i >= 0; i-- {
			rw := wrappedRows(flat[i], r.cols)
			if used+rw > r.maxRows && i < len(flat)-1 {
				break
			}
			used += rw
			start = i
		}
		flat = flat[start:]
	}

	var sb strings.Builder
	sb.WriteString("\x1b[?2026h")
	r.appendClearLocked(&sb)
	rows := 0
	for i, l := range flat {
		if i > 0 {
			sb.WriteString("\r\n")
		}
		sb.WriteString(l)
		rows += wrappedRows(l, r.cols)
	}
	sb.WriteString("\x1b[?2026l")
	_, _ = io.WriteString(r.w, sb.String())
	r.lastRows = rows
}

func wrappedRows(line string, cols int) int {
	if cols <= 0 {
		return 1
	}
	w := width.StringWidth(line)
	if w <= cols {
		return 1
	}
	return (w + cols - 1) / cols
}

// Commit writes permanent lines into scrollback above the live region.
func (r *Region) Commit(text string) {
	if r == nil || r.w == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	_, _ = io.WriteString(r.w, text)
}

// Clear erases the live region.
func (r *Region) Clear() {
	if r == nil || r.w == nil || !r.isTTY {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
}

func (r *Region) clearLocked() {
	if r.lastRows <= 0 {
		return
	}
	var sb strings.Builder
	r.appendClearLocked(&sb)
	_, _ = io.WriteString(r.w, sb.String())
}

func (r *Region) appendClearLocked(sb *strings.Builder) {
	if r.lastRows <= 0 {
		return
	}
	sb.WriteByte('\r')
	for i := 0; i < r.lastRows; i++ {
		sb.WriteString("\x1b[2K")
		if i+1 < r.lastRows {
			sb.WriteString("\x1b[1A")
		}
	}
	sb.WriteByte('\r')
	r.lastRows = 0
}

// StatusLabel tracks spinner status with hysteresis (slow to appear, quick to give way).
type StatusLabel struct {
	cur       string
	pending   string
	changedAt time.Time
}

// Update advances the status label with 350ms hysteresis for non-urgent labels.
func (s *StatusLabel) Update(next string, urgent bool, now time.Time) string {
	if urgent || next == "" {
		s.cur = next
		s.pending = ""
		return s.cur
	}
	if next == s.cur {
		s.pending = ""
		return s.cur
	}
	if s.pending != next {
		s.pending = next
		s.changedAt = now
		return s.cur
	}
	if now.Sub(s.changedAt) >= 350*time.Millisecond {
		s.cur = next
		s.pending = ""
	}
	return s.cur
}

// FormatSpinner renders the persistent status row.
func FormatSpinner(frame int, elapsed time.Duration, label string, dim, reset string) string {
	ch := spinnerFrames[frame%len(spinnerFrames)]
	if label == "" {
		label = "working"
	}
	return fmt.Sprintf("%s%s %s (%s)%s", dim, ch, label, elapsed.Round(100*time.Millisecond), reset)
}

// StripNewlines collapses newlines for single-line status display.
func StripNewlines(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
