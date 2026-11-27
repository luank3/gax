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
	lastRows int
}

// NewRegion returns a live-region writer over w.
func NewRegion(w io.Writer, isTTY bool) *Region {
	return &Region{w: w, isTTY: isTTY, cols: 80}
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

// Paint replaces the currently painted live region with lines, accounting for wrapping.
func (r *Region) Paint(lines ...string) {
	if r == nil || r.w == nil || !r.isTTY {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	rows := 0
	for i, l := range flat {
		if i > 0 {
			_, _ = io.WriteString(r.w, "\r\n")
		}
		_, _ = io.WriteString(r.w, l)
		rows += wrappedRows(l, r.cols)
	}
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
	_, _ = io.WriteString(r.w, "\r")
	for i := 0; i < r.lastRows; i++ {
		_, _ = io.WriteString(r.w, "\x1b[2K")
		if i+1 < r.lastRows {
			_, _ = io.WriteString(r.w, "\x1b[1A")
		}
	}
	_, _ = io.WriteString(r.w, "\r")
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
