package live_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/tui/live"
)

func TestRegionWrappedClearCount(t *testing.T) {
	var buf bytes.Buffer
	r := live.NewRegion(&buf, true)
	r.SetCols(10)
	// "123456789012345" is 15 cols -> wraps to 2 rows at cols=10.
	r.Paint("123456789012345")
	buf.Reset()
	r.Clear()
	// Clearing 2 rows requires one cursor-up (\x1b[1A) and two \x1b[2K.
	got := buf.String()
	if strings.Count(got, "\x1b[2K") != 2 || !strings.Contains(got, "\x1b[1A") {
		t.Fatalf("unexpected wrapped clear sequence: %q", got)
	}
}
