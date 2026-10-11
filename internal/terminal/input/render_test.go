package input_test

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/terminal/input"
)

func TestCoreRenderLayout(t *testing.T) {
	cases := []struct {
		name            string
		text            string
		cursor          int
		prompt, columns int
		want            input.Layout
	}{
		{name: "empty", prompt: 2, columns: 80, want: input.Layout{CursorRow: 0, CursorColumn: 2, EndRow: 0, EndColumn: 2, TotalRows: 1}},
		{name: "single line", text: "hello", cursor: 3, prompt: 2, columns: 80, want: input.Layout{CursorRow: 0, CursorColumn: 5, EndRow: 0, EndColumn: 7, TotalRows: 1}},
		{name: "hard newline", text: "ab\ncd", cursor: 2, prompt: 2, columns: 80, want: input.Layout{CursorRow: 0, CursorColumn: 4, EndRow: 1, EndColumn: 4, TotalRows: 2}},
		{name: "cursor after newline", text: "ab\n", cursor: 3, prompt: 2, columns: 80, want: input.Layout{CursorRow: 1, CursorColumn: 2, EndRow: 1, EndColumn: 2, TotalRows: 2}},
		{name: "tab", text: "\tx", cursor: 2, prompt: 2, columns: 80, want: input.Layout{CursorRow: 0, CursorColumn: 7, EndRow: 0, EndColumn: 7, TotalRows: 1}},
		{name: "char wrap", text: "0123456789", cursor: 10, prompt: 2, columns: 10, want: input.Layout{CursorRow: 1, CursorColumn: 5, EndRow: 1, EndColumn: 5, TotalRows: 2}},
		{name: "word wrap", text: "hello world", cursor: 11, prompt: 2, columns: 10, want: input.Layout{CursorRow: 1, CursorColumn: 7, EndRow: 1, EndColumn: 7, TotalRows: 2}},
		{name: "drop boundary space", text: "abcdefg ij", cursor: 10, prompt: 2, columns: 10, want: input.Layout{CursorRow: 1, CursorColumn: 4, EndRow: 1, EndColumn: 4, TotalRows: 2}},
		{name: "continuation indent", text: "a\nb\nc", cursor: 5, prompt: 2, columns: 80, want: input.Layout{CursorRow: 2, CursorColumn: 3, EndRow: 2, EndColumn: 3, TotalRows: 3}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			core := input.NewCore()
			core.SetBuffer(test.text)
			core.SetCursor(test.cursor)
			got := core.Render(test.prompt, test.prompt, test.columns, nil)
			if got != test.want {
				t.Fatalf("Render layout = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCoreRenderWrapEvents(t *testing.T) {
	cases := []struct {
		name string
		text string
		cols int
		want string
	}{
		{name: "word", text: "hello world", cols: 10, want: "hello /world"},
		{name: "drop boundary space", text: "abcdefg ij", cols: 10, want: "abcdefg/ij"},
		{name: "character fallback", text: "abcdefghijk", cols: 10, want: "abcdefg/hijk"},
		{name: "long word after wrap", text: "hello abcdefghijk", cols: 10, want: "hello /abcdefg/hijk"},
		{name: "tab and controls", text: "a\tb\ac", cols: 80, want: "a    b?c"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			core := input.NewCore()
			core.SetBuffer(test.text)
			var got strings.Builder
			prompt, continuation := 2, 2
			if test.name == "tab and controls" {
				prompt, continuation = 0, 0
			}
			layout := core.Render(prompt, continuation, test.cols, func(event input.RenderEvent) {
				if event.Kind == input.RenderRowBreak {
					got.WriteByte('/')
				} else {
					got.WriteString(event.Bytes)
				}
			})
			if got.String() != test.want {
				t.Fatalf("render output = %q, layout %+v; want %q", got.String(), layout, test.want)
			}
		})
	}
}

func TestCoreRenderWindowForwardsSelectedRowsAndFullLayout(t *testing.T) {
	core := input.NewCore()
	core.SetBuffer("a\nb\nc\nd\ne")
	core.SetCursor(len(core.Text()))
	var got strings.Builder
	layout := core.RenderWindow(0, 0, 80, 1, 3, func(event input.RenderEvent) {
		if event.Kind == input.RenderRowBreak {
			got.WriteByte('/')
		} else {
			got.WriteString(event.Bytes)
		}
	})
	if got.String() != "/b/c/d" {
		t.Fatalf("window output = %q, want %q", got.String(), "/b/c/d")
	}
	if layout.TotalRows != 5 || layout.CursorRow != 4 {
		t.Fatalf("window layout = %+v", layout)
	}
	if got := input.WindowTop(20, 19, 30, 10); got != 19 {
		t.Fatalf("WindowTop() = %d, want 19", got)
	}
}

func TestCoreRenderSubstitutesBidiAndC1Controls(t *testing.T) {
	for _, text := range []string{"\u202e", "\u061c", "\u2060", "\u009b"} {
		core := input.NewCore()
		core.SetBuffer(text)
		var got strings.Builder
		layout := core.Render(0, 0, 80, func(event input.RenderEvent) {
			if event.Kind == input.RenderGlyph {
				got.WriteString(event.Bytes)
			}
		})
		if got.String() != "?" || layout.EndColumn != 1 {
			t.Errorf("render %U = %q, layout %+v; want one safe substitute cell", []rune(text), got.String(), layout)
		}
	}
}

func TestCoreRenderMalformedUTF8IsSubstitutedBytewise(t *testing.T) {
	core := input.NewCore()
	core.SetBuffer("\xc3(")
	var got strings.Builder
	layout := core.Render(0, 0, 80, func(event input.RenderEvent) {
		if event.Kind == input.RenderGlyph {
			got.WriteString(event.Bytes)
		}
	})
	if got.String() != "?(" || layout.EndColumn != 2 {
		t.Fatalf("malformed render = %q, layout %+v", got.String(), layout)
	}
}

func TestCoreRenderKeepsCombiningMarkAfterItsFullPendingHostBatch(t *testing.T) {
	text := strings.Repeat("a", 256) + "\u0301"
	core := input.NewCore()
	core.SetBuffer(text)
	aCount := 0
	combiningAfter := -1
	core.Render(0, 0, 0, func(event input.RenderEvent) {
		if event.Kind != input.RenderGlyph {
			return
		}
		if event.Bytes == "a" {
			aCount++
		} else if event.Width == 0 && combiningAfter < 0 {
			combiningAfter = aCount
		}
	})
	if aCount != 256 || combiningAfter != 256 {
		t.Fatalf("host/combining order = %d/%d", aCount, combiningAfter)
	}
}

func TestCoreRenderWindowForwardsSoftWrapIntoSelectedRows(t *testing.T) {
	core := input.NewCore()
	core.SetBuffer("hello world")
	var got strings.Builder
	core.RenderWindow(2, 2, 10, 1, 1, func(event input.RenderEvent) {
		if event.Kind == input.RenderRowBreak {
			got.WriteByte('/')
		} else {
			got.WriteString(event.Bytes)
		}
	})
	if got.String() != "/world" {
		t.Fatalf("soft-wrapped window = %q, want %q", got.String(), "/world")
	}
}
