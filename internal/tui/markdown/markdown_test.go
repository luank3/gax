package markdown_test

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/tui/markdown"
	"github.com/luank3/gax/internal/tui/theme"
)

func TestRenderTableAndLists(t *testing.T) {
	pal := theme.Resolve("dark", "", true)
	src := "# Heading\n\n---\n\n| Name | Role |\n| --- | --- |\n| gax | agent |\n\n> note\n- item\n  1. sub\n\nSee [docs](https://example.com) and *em* and ~~old~~."
	got := markdown.Render(src, 80, pal, true)
	for _, want := range []string{"Heading", "────", "Name", "Role", "─┼─", "gax", "agent", "│ ", "note", "• ", "1.", "sub", "docs", "(https://example.com)", "\x1b[3mem\x1b[23m", "\x1b[9mold\x1b[29m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in rendered markdown:\n%s", want, got)
		}
	}
}
