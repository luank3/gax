package markdown

import (
	"errors"
	"strings"
	"testing"
)

func TestStreamEmitsPlainProseBeforeClose(t *testing.T) {
	var output strings.Builder
	stream := NewStream(0, true, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("alpha "); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "alpha " {
		t.Fatalf("output before next feed = %q, want %q", got, "alpha ")
	}
	if err := stream.Feed("beta"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "alpha beta" {
		t.Fatalf("output before close = %q, want %q", got, "alpha beta")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != Render("alpha beta", 0, true) {
		t.Fatalf("stream output = %q, Render = %q", got, Render("alpha beta", 0, true))
	}
}

func TestStreamEmitsProseBreakOnceNextLineIsAList(t *testing.T) {
	var output strings.Builder
	stream := NewStream(100, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("alpha\n* "); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "alpha\n" {
		t.Fatalf("output before list item completes = %q, want the definite prose break", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "alpha\n* " {
		t.Fatalf("final provisional list marker = %q, want the marker on its own line", got)
	}
}

func TestStreamEmitsSoftJoinedProseAcrossPhysicalLines(t *testing.T) {
	var output strings.Builder
	stream := NewStream(100, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("alpha\nbeta gamma "); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "alpha beta gamma " {
		t.Fatalf("soft-joined prose before close = %q, want the eager wrapped prefix", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render("alpha\nbeta gamma ", 100, false); got != want {
		t.Fatalf("soft-joined stream output = %q, want %q", got, want)
	}
}

func TestStreamEmitsCompletedWrappedWordsBeforeBoundary(t *testing.T) {
	var output strings.Builder
	stream := NewStream(12, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("alpha beta "); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "alpha beta "; got != want {
		t.Fatalf("wrapped prose before boundary = %q, want eager wrapped output %q", got, want)
	}
	if err := stream.Feed("gamma delta "); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render("alpha beta gamma delta ", 12, false); got != want {
		t.Fatalf("wrapped stream output = %q, want Render output %q", got, want)
	}
}

func TestStreamEmitsOpenWrappedWordEagerly(t *testing.T) {
	var output strings.Builder
	stream := NewStream(7, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("hello"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "hello" {
		t.Fatalf("wrapped prose before a break = %q, want eagerly emitted text", got)
	}
	if err := stream.Feed(" world"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render("hello world", 7, false); got != want {
		t.Fatalf("wrapped stream output = %q, want Render output %q", got, want)
	}
}

func TestStreamDefersAmbiguousInlineMarkerAndHandlesSplitUTF8(t *testing.T) {
	var output strings.Builder
	stream := NewStream(0, true, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("hello **bo"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "hello " {
		t.Fatalf("output with open emphasis = %q, want %q", got, "hello ")
	}
	if err := stream.Feed("ld** "); err != nil {
		t.Fatal(err)
	}
	if err := stream.Feed(string([]byte{0xe2, 0x80})); err != nil {
		t.Fatal(err)
	}
	if err := stream.Feed(string([]byte{0x94}) + " done"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	want := Render("hello **bold** — done", 0, true)
	if got := output.String(); got != want {
		t.Fatalf("stream output = %q, want Render output %q", got, want)
	}
}

func TestStreamBuffersTablesUntilTheirBoundary(t *testing.T) {
	var output strings.Builder
	stream := NewStream(40, true, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("| Name | Age |\n|---|---|\n| Bob | 30 |\n"); err != nil {
		t.Fatal(err)
	}
	if !stream.InTable() {
		t.Fatal("stream did not report a buffered table")
	}
	if got := output.String(); got != "" {
		t.Fatalf("table was emitted before its boundary: %q", got)
	}
	if err := stream.Feed("after\n"); err != nil {
		t.Fatal(err)
	}
	if stream.InTable() {
		t.Fatal("stream still reports table buffering after boundary")
	}
	if got := output.String(); !strings.Contains(got, "Name") || !strings.Contains(got, "Bob") {
		t.Fatalf("table was not rendered at boundary: %q", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render("| Name | Age |\n|---|---|\n| Bob | 30 |\nafter\n", 40, true); got != want {
		t.Fatalf("stream output = %q, want Render output %q", got, want)
	}
}

func TestStreamBailsOversizedUnterminatedTableRowLosslessly(t *testing.T) {
	var output strings.Builder
	stream := NewStream(40, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	input := "| H |\n|---|\n| " + strings.Repeat("a", 70000) + " |"
	if err := stream.Feed(input); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.HasPrefix(got, "| H |\n|---|\n|") || !strings.Contains(got, strings.Repeat("a", 100)) {
		t.Fatalf("oversized table output lost its literal header or row body: %q", got[:min(len(got), 80)])
	}
	if stream.InTable() {
		t.Fatal("stream remained in table collection after the byte cap bailout")
	}
}

func TestStreamResetDiscardsPendingInputAndCanBeReused(t *testing.T) {
	var output strings.Builder
	stream := NewStream(0, true, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("**discarded"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Feed("kept"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "kept" {
		t.Fatalf("output after reset = %q, want %q", got, "kept")
	}
}

func TestStreamStyledModeTransitionFlushesAtCallerOwnedSeams(t *testing.T) {
	var output strings.Builder
	stream := NewStream(5, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("**quiet**"); err != nil {
		t.Fatal(err)
	}
	if err := stream.SetStyled(true); err != nil {
		t.Fatal(err)
	}
	output.WriteByte('\n')
	if err := stream.Feed("## H\n"); err != nil {
		t.Fatal(err)
	}
	if err := stream.SetStyled(false); err != nil {
		t.Fatal(err)
	}
	if err := stream.Feed("abcde "); err != nil {
		t.Fatal(err)
	}
	if err := stream.SetStyled(true); err != nil {
		t.Fatal(err)
	}
	output.WriteByte('\n')
	if err := stream.Feed("x"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	want := "quiet\n\x1b[1mH\x1b[22m\nabcde\nx"
	if got := output.String(); got != want {
		t.Fatalf("styled mode transition output = %q, want %q", got, want)
	}
}

func TestStreamLatchesWriterError(t *testing.T) {
	wantErr := errors.New("terminal write failed")
	writes := 0
	stream := NewStream(0, true, func(string) error {
		writes++
		return wantErr
	})
	if err := stream.Feed("plain "); !errors.Is(err, wantErr) {
		t.Fatalf("Feed error = %v, want %v", err, wantErr)
	}
	if err := stream.Feed("later"); !errors.Is(err, wantErr) {
		t.Fatalf("later Feed error = %v, want %v", err, wantErr)
	}
	if err := stream.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close error = %v, want %v", err, wantErr)
	}
	if writes != 1 {
		t.Fatalf("writer calls after error = %d, want 1", writes)
	}
}

func TestStreamOutputMatchesRenderAcrossChunkPartitions(t *testing.T) {
	fixtures := []struct {
		input  string
		width  int
		styled bool
	}{
		{"plain **bold** and _italic_ with `code`", 0, true},
		{"first line\nsecond line\n\n- item\n", 0, false},
		{"before—after https://example.com/docs.", 0, true},
		{"foo\r\nbar", 0, false},
		{"foo\n   bar", 7, false},
		{"foo  \r\nbar", 0, false},
		{"intro\n    - looks like marker", 0, false},
		{"intro\n* ", 0, false},
		{"* alpha beta gamma delta\nepsilon zeta eta theta iota", 20, true},
		{"hello world", 7, false},
		{"**foo bar**baz", 8, true},
		{"héllo wörld", 8, false},
		{"| Name | Note |\n|---|---|\n| A | **bold** |\n", 40, true},
		{"| L | CC | RR |\n|:---|:---:|---:|\n| x | y | z |", 40, true},
		{"Intro\n| H |\n|---|\n| x |\nAfter\n", 40, false},
		{"one two three four five", 12, false},
		{"first line\nsecond line\n\n  - **item**\n\n## Heading\n", 0, true},
		{"#### Section\n- one\n- two\n", 0, true},
		{"## Head\n\nbody text\n", 0, true},
		{"````markdown\n```c\nx = 1;\n```\n````\n", 0, true},
		{"- alpha **bravo charlie** delta echo foxtrot", 16, true},
	}
	for _, fixture := range fixtures {
		for split := 0; split <= len(fixture.input); split++ {
			var output strings.Builder
			stream := NewStream(fixture.width, fixture.styled, func(chunk string) error {
				output.WriteString(chunk)
				return nil
			})
			if err := stream.Feed(fixture.input[:split]); err != nil {
				t.Fatalf("Feed(%q before split %d): %v", fixture.input, split, err)
			}
			if err := stream.Feed(fixture.input[split:]); err != nil {
				t.Fatalf("Feed(%q after split %d): %v", fixture.input, split, err)
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("Close(%q split %d): %v", fixture.input, split, err)
			}
			if got, want := output.String(), Render(fixture.input, fixture.width, fixture.styled); got != want {
				t.Fatalf("stream(%q, split %d) = %q, want %q", fixture.input, split, got, want)
			}
		}
		var output strings.Builder
		stream := NewStream(fixture.width, fixture.styled, func(chunk string) error {
			output.WriteString(chunk)
			return nil
		})
		for index := 0; index < len(fixture.input); index++ {
			if err := stream.Feed(fixture.input[index : index+1]); err != nil {
				t.Fatalf("bytewise Feed(%q at %d): %v", fixture.input, index, err)
			}
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("bytewise Close(%q): %v", fixture.input, err)
		}
		if got, want := output.String(), Render(fixture.input, fixture.width, fixture.styled); got != want {
			t.Fatalf("bytewise stream(%q) = %q, want %q", fixture.input, got, want)
		}
	}
}

func TestStreamMatchesUpstreamSplitFeedFixtures(t *testing.T) {
	fixtures := []struct {
		name   string
		feeds  []string
		styled bool
		want   string
	}{
		{"split h4 heading", []string{"###", "# title\n"}, true, ansiBold + "title" + ansiBoldOff + "\n"},
		{"fence info string", []string{"```py", "thon\nint x;\n```\n"}, true, ansiDim + "int x;\n" + ansiBoldOff},
		{"long split fence info", []string{"```python title=\"some/very/long/", "path/here.py\"\nx = 1\n```\n"}, true, ansiDim + "x = 1\n" + ansiBoldOff},
		{"padded ordered marker", []string{"1.  ", "  foo"}, true, ansiDim + "1. " + ansiBoldOff + "foo"},
		{"split bold delimiter", []string{"*", "*hi*", "*"}, true, ansiBold + "hi" + ansiBoldOff},
		{"split inline code", []string{"x ", "`", "y`"}, true, "x " + ansiCyan + "y" + ansiFGDefault},
		{"split fence opener", []string{"`", "`", "`\nbody\n```\n"}, true, ansiDim + "body\n" + ansiBoldOff},
		{"split indented incomplete fence", []string{"   `", "```"}, true, "````"},
		{"short fence candidate", []string{"`", "\n"}, true, ansiCyan + ansiFGDefault + "\n"},
		{"tight em dash", []string{"Yes—", "this"}, false, "Yes — this"},
		{"soft break", []string{"foo\n", "bar"}, false, "foo bar"},
		{"paragraph break", []string{"foo\n", "\nbar"}, false, "foo\n\nbar"},
		{"hard break spaces", []string{"foo  ", "\nbar"}, false, "foo  \nbar"},
		{"blank run across feeds", []string{"a\n\n", "\n\nb"}, false, "a\n\nb"},
		{"intraword underscore across feeds", []string{"compile", "_commands"}, true, "compile_commands"},
		{"word-boundary underscore across feeds", []string{"say ", "_yes_"}, true, "say " + ansiItalic + "yes" + ansiItalicOff},
		{"split link scheme", []string{"go http", "s://example.com/x yes"}, true, "go " + ansiUnderline + "https://example.com/x" + ansiUnderlineOff + " yes"},
		{"provisional bullet at eof", []string{"- "}, false, "- "},
		{"ordered marker at eof", []string{"1. "}, true, ansiDim + "1. " + ansiBoldOff},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var output strings.Builder
			stream := NewStream(0, fixture.styled, func(chunk string) error {
				output.WriteString(chunk)
				return nil
			})
			for _, feed := range fixture.feeds {
				if err := stream.Feed(feed); err != nil {
					t.Fatalf("Feed(%q): %v", feed, err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != fixture.want {
				t.Fatalf("split-feed output = %q, want upstream fixture %q", got, fixture.want)
			}
		})
	}
}

func TestStreamCloseResolvesUpstreamEOFStyles(t *testing.T) {
	fixtures := []struct {
		input  string
		styled bool
		want   string
	}{
		{"**bold", true, ansiBold + "bold" + ansiBoldOff},
		{"*hi*", true, ansiItalic + "hi" + ansiItalicOff},
		{"_hi_", true, ansiItalic + "hi" + ansiItalicOff},
		{"foo*", true, "foo*"},
		{"```\nfoo\n```", true, ansiDim + "foo\n" + ansiBoldOff},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.input, func(t *testing.T) {
			var output strings.Builder
			stream := NewStream(0, fixture.styled, func(chunk string) error {
				output.WriteString(chunk)
				return nil
			})
			if err := stream.Feed(fixture.input); err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != fixture.want {
				t.Fatalf("Close output = %q, want upstream EOF fixture %q", got, fixture.want)
			}
		})
	}
}

func TestStreamBoundaryResolvesSyntaxAndLeavesStreamReusable(t *testing.T) {
	var output strings.Builder
	stream := NewStream(0, true, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err := stream.Feed("**first"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "" {
		t.Fatalf("open emphasis emitted before its boundary: %q", got)
	}
	if err := stream.Boundary(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Feed("second"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), ansiBold+"first"+ansiBoldOff+"second"; got != want {
		t.Fatalf("boundary reuse output = %q, want %q", got, want)
	}
}

func TestStreamBoundarySettlesBufferedTableAndClearsTableState(t *testing.T) {
	var output strings.Builder
	stream := NewStream(40, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	input := "| H | V |\n|---|---|\n| x | y |\n"
	if err := stream.Feed(input); err != nil {
		t.Fatal(err)
	}
	if !stream.InTable() || output.Len() != 0 {
		t.Fatalf("table state before Boundary: inTable=%v output=%q", stream.InTable(), output.String())
	}
	if err := stream.Boundary(); err != nil {
		t.Fatal(err)
	}
	if stream.InTable() || output.Len() == 0 {
		t.Fatalf("table state after Boundary: inTable=%v output=%q", stream.InTable(), output.String())
	}
	if err := stream.Feed("after"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render(input, 40, false)+"after"; got != want {
		t.Fatalf("table boundary output = %q, want %q", got, want)
	}
}

func TestStreamTracksTableAfterEagerProse(t *testing.T) {
	var output strings.Builder
	stream := NewStream(40, false, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	input := "Intro\n| H |\n|---|\n| x |\n"
	if err := stream.Feed(input); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "Intro\n" {
		t.Fatalf("eager prose before buffered table = %q, want %q", got, "Intro\n")
	}
	if !stream.InTable() {
		t.Fatal("stream did not report a table after eager prose")
	}
	if err := stream.Feed("After"); err != nil {
		t.Fatal(err)
	}
	if stream.InTable() {
		t.Fatal("stream still reports a table after the following block began")
	}
	if got := output.String(); got != "Intro\n" {
		t.Fatalf("table was flushed before the following line completed: %q", got)
	}
	if err := stream.Feed("\n"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), Render(input+"After\n", 40, false); got != want {
		t.Fatalf("stream with a table after prose = %q, want %q", got, want)
	}
}

func TestRenderUpstreamSpacesTightEmDash(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Yes—this works", "Yes — this works"},
		{"**word—** next", ansiBold + "word " + "—" + ansiBoldOff + " next"},
		{"word—**bold**", "word —" + ansiBold + " bold" + ansiBoldOff},
		{"`a—b`", ansiCyan + "a—b" + ansiFGDefault},
		{"docs—https://x.com", "docs — " + ansiUnderline + "https://x.com" + ansiUnderlineOff},
		{"**unfinished", ansiBold + "unfinished" + ansiBoldOff},
		{"Heading\n=====", "Heading\n====="},
	}
	for _, test := range cases {
		if got := Render(test.input, 0, true); got != test.want {
			t.Errorf("Render(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
