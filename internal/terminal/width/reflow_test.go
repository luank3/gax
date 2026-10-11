package width

import (
	"strings"
	"testing"
)

func TestReflowForDisplayMatchesUpstreamFixtures(t *testing.T) {
	tests := []struct {
		name, input                 string
		first, other, rows, reserve int
		want                        string
	}{
		{"word break", "hello world more", 10, 10, 2, 0, "hello\nworld more"},
		{"last row ellipsis", "hello world more here", 10, 10, 2, 0, "hello\nworld..."},
		{"hard break", "abcdefghijklmnopqrstuvwxyz", 10, 10, 2, 0, "abcdefghij\nklmnopq..."},
		{"first row narrower", "hello brave new world", 5, 10, 2, 0, "hello\nbrave..."},
		{"wide glyph is not split", "界xxx", 4, 4, 1, 0, "..."},
		{"suffix reserve", "abcdef", 10, 10, 3, 5, "abcde\nf"},
		{"three hard rows truncate", strings.Repeat("a", 40), 10, 10, 3, 0, "aaaaaaaaaa\naaaaaaaaaa\naaaaaaa..."},
		{"three hard rows fit", strings.Repeat("a", 30), 10, 10, 3, 0, "aaaaaaaaaa\naaaaaaaaaa\naaaaaaaaaa"},
		{"slash break", "cat src/render/tool_render.c", 20, 20, 2, 0, "cat src/render/\ntool_render.c"},
		{"spaces preferred", "echo one/two three/four", 20, 20, 2, 0, "echo one/two\nthree/four"},
		{"mark runs retained", "cd build/out&&ls -la", 13, 13, 2, 0, "cd build/\nout&&ls -la"},
		{"combining mark remains with delimiter", "abc=\u0338defghijklmnop", 8, 8, 3, 0, "abc=\u0338\ndefghijk\nlmnop"},
		{"last row keeps delimiter mark", "abc=\u0338defghijklmnop", 11, 11, 1, 0, "abc=\u0338..."},
		{"oversized glyph gets its own row", "界ab", 1, 1, 3, 0, "界\na\nb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ReflowForDisplay(test.input, test.first, test.other, test.rows, test.reserve)
			if got != test.want {
				t.Fatalf("ReflowForDisplay() = %q, want %q", got, test.want)
			}
		})
	}
	spaceSlack := strings.Repeat("a", 21) + " " + strings.Repeat("b", 30)
	if got, want := ReflowForDisplay(spaceSlack, 40, 40, 1, 0), strings.Repeat("a", 21)+"..."; got != want {
		t.Errorf("space break inside slack window = %q, want %q", got, want)
	}
	outsideSlack := strings.Repeat("a", 20) + " " + strings.Repeat("b", 30)
	wantOutside := strings.Repeat("a", 20) + " " + strings.Repeat("b", 16) + "..."
	if got := ReflowForDisplay(outsideSlack, 40, 40, 1, 0); got != wantOutside {
		t.Errorf("space break outside slack window = %q, want %q", got, wantOutside)
	}
}
