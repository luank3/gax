package usagerender

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteWindowUsesLocaleAppropriateProgressGlyphs(t *testing.T) {
	window := Window{Label: "weekly", UsedPercent: 50, ResetAt: time.Now()}
	for _, test := range []struct {
		name    string
		utf8    bool
		wantBar string
	}{
		{name: "UTF-8", utf8: true, wantBar: strings.Repeat("█", 10) + strings.Repeat("░", 10)},
		{name: "ASCII", utf8: false, wantBar: strings.Repeat("#", 10) + strings.Repeat("-", 10)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writeWindow(&output, window, 100, test.utf8)
			if !strings.Contains(output.String(), test.wantBar) {
				t.Fatalf("WriteWindow() = %q, want bar %q", output.String(), test.wantBar)
			}
		})
	}
}
