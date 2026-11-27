package markdown_test

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/tui/markdown"
	"github.com/luank3/gax/internal/tui/theme"
)

func TestRenderTableAndLists(t *testing.T) {
	pal := theme.Resolve("none", "", false)
	src := "| Name | Role |\n| --- | --- |\n| gax | agent |\n\n> note\n- item"
	got := markdown.Render(src, 80, pal, true)
	for _, want := range []string{"Name", "Role", "─┼─", "gax", "agent", "│ note", "  • item"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in rendered markdown:\n%s", want, got)
		}
	}
}
