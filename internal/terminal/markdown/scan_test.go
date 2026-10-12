package markdown

import (
	"strings"
	"testing"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
)

func TestScanLineClassifiesBlockPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		final bool
		want  LineInfo
	}{
		{name: "empty prefix", line: "", want: LineInfo{Kind: LineIncomplete}},
		{name: "open whitespace prefix", line: "   ", want: LineInfo{Kind: LineIncomplete, IndentLength: 3}},
		{name: "blank whitespace line", line: " \t\n", want: LineInfo{Kind: LineBlank, IndentLength: 2, Complete: true, NormalizeIndent: true}},
		{name: "ordinary text", line: "plain", want: LineInfo{Kind: LineText, Complete: true}},
		{name: "four-space heading is text", line: "    # heading\n", want: LineInfo{Kind: LineText, IndentLength: 4, Complete: true}},
		{name: "heading", line: "  ### title\n", want: LineInfo{Kind: LineHeading, IndentLength: 2, MarkerLength: 3, Marker: '#', Complete: true, NormalizeIndent: true}},
		{name: "six hash heading", line: "######\n", want: LineInfo{Kind: LineHeading, MarkerLength: 6, Marker: '#', Complete: true, NormalizeIndent: true}},
		{name: "seven hashes are text", line: "#######\n", want: LineInfo{Kind: LineText, Complete: true}},
		{name: "hash without separator", line: "##not\n", want: LineInfo{Kind: LineText, Complete: true}},
		{name: "fence info", line: "```go\n", want: LineInfo{Kind: LineFence, MarkerLength: 3, Marker: '`', Complete: true, NormalizeIndent: true}},
		{name: "indented fence", line: " ```c\n", want: LineInfo{Kind: LineFence, IndentLength: 1, MarkerLength: 3, Marker: '`', Complete: true, NormalizeIndent: true}},
		{name: "bullet", line: "- item\n", want: LineInfo{Kind: LineBullet, MarkerLength: 1, Marker: '-', Complete: true}},
		{name: "provisional bullet", line: "* ", want: LineInfo{Kind: LineBullet, MarkerLength: 1, Marker: '*'}},
		{name: "complete star bullet", line: "* item", want: LineInfo{Kind: LineBullet, MarkerLength: 1, Marker: '*', Complete: true}},
		{name: "plus bullet", line: "+ ", want: LineInfo{Kind: LineBullet, MarkerLength: 1, Marker: '+', Complete: true}},
		{name: "indented thematic break", line: "  * * *\n", want: LineInfo{Kind: LineThematic, IndentLength: 2, MarkerLength: 3, Marker: '*', Complete: true, NormalizeIndent: true}},
		{name: "thematic candidate", line: "---", want: LineInfo{Kind: LineIncomplete}},
		{name: "thematic marker with suffix", line: "---x\n", want: LineInfo{Kind: LineText, Complete: true}},
		{name: "four-space bullet", line: "    * item", want: LineInfo{Kind: LineBullet, IndentLength: 4, MarkerLength: 1, Marker: '*', Complete: true}},
		{name: "tab-indented bullet", line: "\t* item", want: LineInfo{Kind: LineText, IndentLength: 1, Complete: true}},
		{name: "ordered", line: "12) item\n", want: LineInfo{Kind: LineOrdered, MarkerLength: 3, Marker: ')', Complete: true}},
		{name: "indented ordered", line: "  12) item", want: LineInfo{Kind: LineOrdered, IndentLength: 2, MarkerLength: 3, Marker: ')', Complete: true}},
		{name: "four-space ordered", line: "    1. item", want: LineInfo{Kind: LineOrdered, IndentLength: 4, MarkerLength: 2, Marker: '.', Complete: true}},
		{name: "ordered digit prefix", line: "1", want: LineInfo{Kind: LineIncomplete}},
		{name: "ordered delimiter candidate", line: "1x", want: LineInfo{Kind: LineIncomplete}},
		{name: "ordered candidate becomes text", line: "1xy", want: LineInfo{Kind: LineText, Complete: true}},
		{name: "quote", line: "> quoted\n", want: LineInfo{Kind: LineBlockquote, MarkerLength: 1, Marker: '>', Complete: true, NormalizeIndent: true}},
		{name: "indented quote", line: "  > quote", want: LineInfo{Kind: LineBlockquote, IndentLength: 2, MarkerLength: 1, Marker: '>', Complete: true, NormalizeIndent: true}},
		{name: "four-space quote is text", line: "    > quote", want: LineInfo{Kind: LineText, IndentLength: 4, Complete: true}},
		{name: "pipe", line: "| A |\n", want: LineInfo{Kind: LinePipe, MarkerLength: 1, Marker: '|', Complete: true, NormalizeIndent: true}},
		{name: "indented pipe", line: "   | A |", want: LineInfo{Kind: LinePipe, IndentLength: 3, MarkerLength: 1, Marker: '|', Complete: true, NormalizeIndent: true}},
		{name: "thematic", line: "---\n", want: LineInfo{Kind: LineThematic, MarkerLength: 3, Marker: '-', Complete: true, NormalizeIndent: true}},
		{name: "final thematic equals", line: "===", final: true, want: LineInfo{Kind: LineThematic, MarkerLength: 3, Marker: '=', Complete: true, NormalizeIndent: true}},
		{name: "plain hash", line: "#define X", final: true, want: LineInfo{Kind: LineText, Complete: true}},
		{name: "final ambiguous star bullet", line: "* ", final: true, want: LineInfo{Kind: LineText, Complete: true}},
		{name: "final plus bullet", line: "+ ", final: true, want: LineInfo{Kind: LineBullet, MarkerLength: 1, Marker: '+', Complete: true}},
		{name: "final ordered marker", line: "1. ", final: true, want: LineInfo{Kind: LineOrdered, MarkerLength: 2, Marker: '.', Complete: true}},
		{name: "final dangling ordered delimiter", line: "1.", final: true, want: LineInfo{Kind: LineText, Complete: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ScanLine(test.line, test.final); got != test.want {
				t.Fatalf("ScanLine(%q, %t) = %#v, want %#v", test.line, test.final, got, test.want)
			}
		})
	}
}

func TestStyledMarkdownUsesConfiguredThemeRoles(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "violet")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())

	got := Render("# Heading `code` https://example.test", 0, true)
	for _, sequence := range []string{
		"\x1b[1m\x1b[38;5;140mHeading", "\x1b[38;5;140mcode\x1b[39m",
		"\x1b[4;38;5;97mhttps://example.test\x1b[24;39m",
	} {
		if !strings.Contains(got, sequence) {
			t.Errorf("styled markdown omitted theme sequence %q: %q", sequence, got)
		}
	}
}

func TestScanLineDefersAmbiguousPrefixes(t *testing.T) {
	for _, line := range []string{"###", "*", "---"} {
		got := ScanLine(line, false)
		if got.Kind != LineIncomplete || got.Complete {
			t.Errorf("ScanLine(%q, false) = %#v, want incomplete", line, got)
		}
	}
	if got := ScanLine("```", false); got.Kind != LineFence || got.Complete {
		t.Errorf("open fence = %#v, want incomplete fence", got)
	}
	if got := ScanLine("###", true); got.Kind != LineText || !got.Complete {
		t.Errorf("final hash run = %#v, want complete text", got)
	}
	if got := ScanLine("---", true); got.Kind != LineThematic || !got.Complete {
		t.Errorf("final thematic run = %#v, want thematic break", got)
	}
}

func TestScanLineTabIndentDoesNotOpenBlocks(t *testing.T) {
	if got := ScanLine("\t# title\n", true); got.Kind != LineText {
		t.Fatalf("tab-indented heading = %#v, want text", got)
	}
	if got := ScanLine("\t- item\n", true); got.Kind != LineText {
		t.Fatalf("tab-indented bullet = %#v, want text", got)
	}
	if got := ScanLine("    * item\n", true); got.Kind != LineBullet || got.IndentLength != 4 {
		t.Fatalf("four-space bullet = %#v, want scanner-recognized marker", got)
	}
}

func TestRenderFormatsInlineMarkdown(t *testing.T) {
	got := Render("I'm **gax**, with `read` and *bash* tools.", 0, true)
	want := "I'm \x1b[1mgax\x1b[22m, with \x1b[36mread\x1b[39m and \x1b[3mbash\x1b[23m tools."
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderTogglesInlineCodeForEachBacktick(t *testing.T) {
	input := "x ```go ## h `code` y"
	want := "x go ## h code y"
	if got := Render(input, 0, false); got != want {
		t.Fatalf("inline backtick runs = %q, want %q", got, want)
	}
}

func TestRenderClosesInlineCodeAtSoftLineBreak(t *testing.T) {
	if got, want := Render("`\n*o", 0, false), " o"; got != want {
		t.Fatalf("inline code across soft break = %q, want %q", got, want)
	}
}

func TestRenderInlineCodeCloseClearsTrailingHardBreakSpaces(t *testing.T) {
	if got, want := Render("foo ``` bar   \n baz", 0, false), "foo  bar   baz"; got != want {
		t.Fatalf("inline-code trailing spaces = %q, want %q", got, want)
	}
}

func TestRenderDoesNotRepeatInlineCodeTrailingSpaceAfterSoftBreak(t *testing.T) {
	want := ansiCyan + " " + ansiFGDefault + "界"
	if got := Render("` \n界", 0, true); got != want {
		t.Fatalf("inline-code soft-break spacing = %q, want %q", got, want)
	}
}

func TestRenderDoesNotLeakInlineBoundaryAfterURL(t *testing.T) {
	if got, want := Render("https://example.com\nnext", 0, false), "https://example.com next"; got != want {
		t.Fatalf("URL across soft break = %q, want %q", got, want)
	}
}

func TestRenderTreatsSoftBoundaryAsWhitespaceForEmphasis(t *testing.T) {
	if got, want := Render("under__\n界", 0, false), "under__ 界"; got != want {
		t.Fatalf("emphasis before soft break = %q, want %q", got, want)
	}
}

func TestRenderDoubleUnderscoreRetainsSingleUnderscoreBehavior(t *testing.T) {
	if got, want := Render("__bold__", 0, true), ansiItalic+ansiItalicOff+"bold__"; got != want {
		t.Fatalf("styled double-underscore text = %q, want %q", got, want)
	}
	if got, want := Render("__bold__", 0, false), "bold__"; got != want {
		t.Fatalf("unstyled double-underscore text = %q, want %q", got, want)
	}
}

func TestRenderSingleStarClosesItalicOpenedByUnderscore(t *testing.T) {
	if got, want := Render("_h*bold*", 0, false), "hbold*"; got != want {
		t.Fatalf("cross-marker italic close = %q, want %q", got, want)
	}
}

func TestRenderSingleStarDoesNotPairWithBoldDelimiter(t *testing.T) {
	if got, want := Render("``*d**bold", 0, false), "d**bold"; got != want {
		t.Fatalf("single star before bold delimiter = %q, want %q", got, want)
	}
}

func TestRenderOtherItalicMarkerClosesAnOpenUnderscore(t *testing.T) {
	if got, want := Render("under__*bold*_", 0, false), "under_bold*_"; got != want {
		t.Fatalf("cross-marker underscore close = %q, want %q", got, want)
	}
}

func TestRenderDoesNotCloseItalicFromInsideInlineCode(t *testing.T) {
	if got, want := Render("*`*``*bold**", 0, false), "**bold**"; got != want {
		t.Fatalf("italic delimiter inside inline code = %q, want %q", got, want)
	}
}

func TestRenderDoesNotUseURLUnderscoresAsItalicClosers(t *testing.T) {
	if got, want := Render("_http://path.__under__plain", 0, false), "http://path.__under__plain"; got != want {
		t.Fatalf("underscore inside URL = %q, want %q", got, want)
	}
}

func TestRenderClosesUnmatchedItalicAfterTrailingNewline(t *testing.T) {
	want := ansiItalic + "x\n" + ansiItalicOff
	if got := Render("_x\n", 0, true); got != want {
		t.Fatalf("unmatched italic at trailing newline = %q, want %q", got, want)
	}
}

func TestRenderClosesUnmatchedItalicAfterFinalHeadingStyle(t *testing.T) {
	want := ansiBold + ansiItalic + "x" + ansiBoldOff + ansiItalicOff
	if got := Render("# _x", 0, true); got != want {
		t.Fatalf("unmatched italic in final heading = %q, want %q", got, want)
	}
}

func TestRenderClosesFinalHeadingItalicAfterTrailingNewline(t *testing.T) {
	want := ansiBold + ansiItalic + "x" + ansiBoldOff + "\n" + ansiItalicOff
	if got := Render("# _x\n", 0, true); got != want {
		t.Fatalf("final heading italic at trailing newline = %q, want %q", got, want)
	}
}

func TestRenderFormatsBlocksAndSoftBreaks(t *testing.T) {
	got := Render("## Tech\nDetails\n\n- one\n1. two\n```go\nx * y\n```\n", 0, true)
	want := "\x1b[1mTech\x1b[22m\n\nDetails\n\n\x1b[2m• \x1b[22mone\n\x1b[2m1. \x1b[22mtwo\n\x1b[2mx * y\n\x1b[22m"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderPreservesTrailingWhitespaceInHeadingWithoutClosingHashes(t *testing.T) {
	if got, want := Render("# title ", 0, false), "title "; got != want {
		t.Fatalf("heading trailing whitespace = %q, want %q", got, want)
	}
}

func TestRenderKeepsOrderedListIndentOutsideMarkerStyle(t *testing.T) {
	want := " " + ansiDim + "2) " + ansiBoldOff + "item"
	if got := Render(" 2) item", 0, true); got != want {
		t.Fatalf("indented ordered item = %q, want %q", got, want)
	}
}

func TestRenderParsesInlineMarkdownInBlockquotesAndRejectedPipeRows(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "> **bold** and _italic_\n\n", want: "> bold and italic\n\n"},
		{input: "|**bold**|\n\n", want: "|bold|\n\n"},
		{input: "   > **bold**\n\n", want: "> bold\n\n"},
		{input: "  |**bold**|\n\n", want: "|bold|\n\n"},
	}
	for _, test := range cases {
		if got := Render(test.input, 0, false); got != test.want {
			t.Errorf("Render(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRenderKeepsRejectedPipeCandidateLiteralBeforeAnotherBlock(t *testing.T) {
	input := "|*x*|\n> quote"
	want := "|*x*|\n> quote"
	if got := Render(input, 0, false); got != want {
		t.Fatalf("rejected pipe candidate = %q, want %q", got, want)
	}
}

func TestRenderLeavesIncompletePipeCandidateLiteralAtEOF(t *testing.T) {
	for _, input := range []string{"|*x*|", "|*x*|\n"} {
		if got := Render(input, 0, false); got != input {
			t.Errorf("Render(%q) = %q, want the unresolved pipe candidate unchanged", input, got)
		}
	}
}

func TestRenderSoftJoinsProseAndPreservesHardBreaks(t *testing.T) {
	if got := Render("foo\n  bar", 0, false); got != "foo bar" {
		t.Fatalf("soft break = %q, want %q", got, "foo bar")
	}
	if got := Render("foo  \nbar", 0, false); got != "foo  \nbar" {
		t.Fatalf("hard break = %q, want %q", got, "foo  \nbar")
	}
	if got := Render("foo  \n  bar", 0, false); got != "foo  \n  bar" {
		t.Fatalf("hard break continuation indent = %q, want preserved indentation", got)
	}
	// A carriage return is kept as a text byte and only the newline is replaced, so a CRLF
	// soft break reads "foo\r bar"; the wrapper drops the CR when wrapping is on.
	if got := Render("foo\r\nbar", 0, false); got != "foo\r bar" {
		t.Fatalf("CRLF soft break = %q, want %q", got, "foo\r bar")
	}
	if got := Render("foo  \r\nbar", 0, false); got != "foo  \r\nbar" {
		t.Fatalf("CRLF hard break = %q, want the original hard line break", got)
	}
	if got := Render("foo\t\nbar", 0, false); got != "foo    bar" {
		t.Fatalf("soft break after tab = %q, want the expanded tab without an extra join", got)
	}
	if got := Render("foo\t  \nbar", 0, false); got != "foo      \nbar" {
		t.Fatalf("hard break after tab = %q, want tab expansion and two trailing spaces", got)
	}
}

func TestRenderSoftJoinsIndentedContinuationWhenWrapping(t *testing.T) {
	for _, width := range []int{7, 10, 20} {
		if got := Render("foo\n   bar", width, false); got != "foo bar" {
			t.Errorf("soft join at width %d = %q, want %q", width, got, "foo bar")
		}
	}
}

func TestRenderJoinsIndentedListContinuation(t *testing.T) {
	input := "* list item\n  indented continuation\n* another item\n  another indent"
	want := "\x1b[2m• \x1b[22mlist item indented continuation\n" +
		"\x1b[2m• \x1b[22manother item another indent"
	if got := Render(input, 0, true); got != want {
		t.Fatalf("joined list continuation = %q, want %q", got, want)
	}
}

func TestRenderSoftJoinConsumesFourSpaceContinuation(t *testing.T) {
	if got := Render("intro\n    - looks like marker", 0, false); got != "intro - looks like marker" {
		t.Fatalf("four-space continuation = %q, want %q", got, "intro - looks like marker")
	}
}

func TestRenderKeepsAmbiguousEOFBulletOnItsOwnLine(t *testing.T) {
	if got := Render("intro\n* ", 0, false); got != "intro\n* " {
		t.Fatalf("EOF ambiguous bullet = %q, want literal marker after hard break", got)
	}
}

func TestRenderReflowsListContinuationsWithHangingIndent(t *testing.T) {
	input := "* alpha beta gamma delta\nepsilon zeta eta theta iota"
	want := "\x1b[2m• \x1b[22malpha beta gamma d\x1b[2D\x1b[K\n" +
		"  delta epsilon zeta\n  eta theta iota"
	if got := Render(input, 20, true); got != want {
		t.Fatalf("wrapped list continuation = %q, want %q", got, want)
	}
}

func TestRenderWrapsBlankLineListContinuationAtItsIndent(t *testing.T) {
	input := "- header\n\n  continuation body long enough to wrap onto a second row here ok"
	want := "\x1b[2m• \x1b[22mheader\n\n" +
		"  continuation body long\n" +
		"  enough to wrap onto a \x1b[1D\x1b[K\n" +
		"  second row here ok"
	if got := Render(input, 24, true); got != want {
		t.Fatalf("blank-line list continuation = %q, want %q", got, want)
	}
}

func TestRenderWrapsNestedListsUnderTheirContentColumn(t *testing.T) {
	input := "* parent\n  - child alpha beta gamma delta epsilon zeta"
	want := "\x1b[2m• \x1b[22mparent\n" +
		"  \x1b[2m• \x1b[22mchild alpha beta g\x1b[2D\x1b[K\n" +
		"    gamma delta epsilo\x1b[7D\x1b[K\n" +
		"    epsilon zeta"
	if got := Render(input, 22, true); got != want {
		t.Fatalf("nested list wrap = %q, want %q", got, want)
	}
}

func TestRenderWrapsAtDisplayWidth(t *testing.T) {
	want := "one two t\x1b[2D\x1b[K\nthree fou\x1b[4D\x1b[K\nfour"
	if got := Render("one two three four", 9, false); got != want {
		t.Fatalf("wrapped prose = %q, want %q", got, want)
	}
}

func TestRenderRetroWrapsWithCursorControl(t *testing.T) {
	if got, want := Render("hello world", 7, false), "hello w\x1b[2D\x1b[K\nworld"; got != want {
		t.Fatalf("plain retro-wrap = %q, want %q", got, want)
	}
	want := ansiBold + "foo bar" + ansiBoldOff + "b\x1b[5D\x1b[K\n" + ansiBold + "bar" + ansiBoldOff + "baz"
	if got := Render("**foo bar**baz", 8, true); got != want {
		t.Fatalf("styled replay wrap = %q, want %q", got, want)
	}
}

func TestRenderMatchesCursorWrapFixtures(t *testing.T) {
	cases := []struct {
		input  string
		width  int
		styled bool
		want   string
	}{
		{"foo bar baz", 7, false, "foo bar\nbaz"},
		{"foo bar \n\nnext", 7, false, "foo bar\n\nnext"},
		{"**alpha beta gamma delta**", 15, true,
			ansiBold + "alpha beta gamm" + "\x1b[5D\x1b[K\n" + "gamma delta" + ansiBoldOff},
		{"foo `code with spaces` bar baz", 15, true,
			"foo " + ansiCyan + "code with s" + "\x1b[2D\x1b[K\n" + "spaces" + ansiFGDefault + " bar baz"},
		{"héllo wörld", 8, false, "héllo wö\x1b[3D\x1b[K\nwörld"},
	}
	for _, test := range cases {
		if got := Render(test.input, test.width, test.styled); got != test.want {
			t.Errorf("Render(%q, %d, %t) = %q, want %q", test.input, test.width, test.styled, got, test.want)
		}
	}
}

func TestRenderReinfersContinuationIndentAfterPendingEdgeWrap(t *testing.T) {
	input := "em_der__ * item  _"
	want := "em_der__\n* item  \x1b[1D\x1b[K\n  _"
	if got := Render(input, 8, false); got != want {
		t.Fatalf("edge-wrap continuation = %q, want %q", got, want)
	}
}

func TestRenderDispatchesShortBacktickCandidateAtLineEnd(t *testing.T) {
	want := ansiCyan + ansiFGDefault + "\n"
	if got := Render("`\n", 0, true); got != want {
		t.Fatalf("short backtick candidate = %q, want %q", got, want)
	}
}

func TestRenderKeepsShortFenceCandidateLiteralAtEOF(t *testing.T) {
	for _, input := range []string{"`", "``", "  `"} {
		if got := Render(input, 0, false); got != input {
			t.Errorf("short fence candidate %q = %q, want it literal", input, got)
		}
	}
}

func TestRenderSoftJoinsShortBacktickAtProseEOF(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"n\n`", "n `"},
		{"n\n``", "n ``"},
	} {
		if got := Render(test.input, 0, false); got != test.want {
			t.Errorf("Render(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRenderKeepsIncompleteFenceInfoLiteralAtEOF(t *testing.T) {
	if got, want := Render("n\n``` a", 0, false), "n\n``` a"; got != want {
		t.Fatalf("incomplete fence info after prose = %q, want %q", got, want)
	}
}

func TestRenderResolvesHeadingSpacingForIncompleteFenceAtEOF(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"## h\n``` a", "h\n\n``` a"},
		{"## h\n ``` a", "h\n``` a"},
	} {
		if got := Render(test.input, 0, false); got != test.want {
			t.Errorf("Render(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRenderDefersIncompleteFenceAfterListContinuation(t *testing.T) {
	input := "1. item\n  continuation\n ``` a"
	want := "1. item conti\x1b[6D\x1b[K\n   continuation\n``` a"
	if got := Render(input, 13, false); got != want {
		t.Fatalf("incomplete fence after list continuation = %q, want %q", got, want)
	}
}

func TestRenderOmitsEmptyFenceBodyAtEOF(t *testing.T) {
	for _, input := range []string{"```\n", "```\n```\n"} {
		if got := Render(input, 0, false); got != "" {
			t.Errorf("empty fenced block %q = %q, want no visible output", input, got)
		}
	}
}

func TestRenderKeepsBareFenceMarkerLiteralAtEOF(t *testing.T) {
	for _, input := range []string{"```", "````"} {
		if got := Render(input, 0, false); got != input {
			t.Errorf("bare fence marker %q = %q, want it literal", input, got)
		}
	}
}

func TestRenderNormalizesIndentedIncompleteFenceAtEOF(t *testing.T) {
	if got := Render("   ````", 0, true); got != "````" {
		t.Fatalf("indented incomplete fence = %q, want literal fence after normalized indent", got)
	}
}

func TestRenderWrapsIndentedIncompleteFenceFallbackAtEOF(t *testing.T) {
	input := " ``` a b c d e f g h i j"
	want := "``` a b \x1b[1D\x1b[K\nc d e f \x1b[1D\x1b[K\ng h i j"
	if got := Render(input, 8, false); got != want {
		t.Fatalf("incomplete fence fallback = %q, want wrapped literal %q", got, want)
	}
}

func TestRenderDropsWhitespaceOnlyEOFContinuation(t *testing.T) {
	if got := Render("foo\n   ", 0, false); got != "foo\n" {
		t.Fatalf("whitespace-only EOF continuation = %q, want one terminating newline", got)
	}
}

func TestRenderKeepsRejectedTableEOFRowLiteral(t *testing.T) {
	want := ansiBold + "H" + ansiBoldOff + "\n" + ansiDim + "─" + ansiBoldOff + "\n*+ ```"
	if got := Render("| H |\n|---|\n*+ ```", 0, true); got != want {
		t.Fatalf("rejected table EOF row = %q, want literal tail %q", got, want)
	}
}

func TestRenderKeepsOversizedWordsIntact(t *testing.T) {
	if got := Render("supercalifragilistic foo", 10, false); got != "supercalifragilistic\nfoo" {
		t.Fatalf("long-word wrap = %q, want the long word intact", got)
	}
}

func TestRenderShrinksDividerToFitNarrowWidth(t *testing.T) {
	want := "a\n\n\x1b[2m·   ·\x1b[22m\n\nb"
	if got := Render("a\n\n***\n\nb", 5, true); got != want {
		t.Fatalf("narrow divider = %q, want %q", got, want)
	}
}

func TestRenderTerminatesThematicBreakAtEOF(t *testing.T) {
	want := "section\n\x1b[2m·   ·   ·\x1b[22m\n"
	if got := Render("section\n---", 0, true); got != want {
		t.Fatalf("EOF thematic break = %q, want %q", got, want)
	}
	if got := Render("---", 0, false); got != "·   ·   ·\n" {
		t.Fatalf("standalone EOF thematic break = %q, want divider plus newline", got)
	}
	if got := Render("Heading\n-----", 0, false); got != "Heading\n·   ·   ·\n" {
		t.Fatalf("EOF setext h2/thematic break = %q", got)
	}
}

func TestRenderCanSuppressStyles(t *testing.T) {
	if got := Render("**bold** `code`", 0, false); got != "bold code" {
		t.Fatalf("unstyled inline = %q, want %q", got, "bold code")
	}
}

func TestRenderRestoresHeadingStyleAfterInlineBold(t *testing.T) {
	want := "\x1b[1mHeading \x1b[1mbold\x1b[22m\x1b[1m tail\x1b[22m"
	if got := Render("## Heading **bold** tail", 0, true); got != want {
		t.Fatalf("heading inline bold = %q, want %q", got, want)
	}
}

func TestRenderExpandsTabsInsideCodeFences(t *testing.T) {
	got := Render("```\n\thello\n```\n", 0, true)
	want := "\x1b[2m    hello\n\x1b[22m"
	if got != want {
		t.Fatalf("fenced tab = %q, want four spaces and fenced style", got)
	}
}

func TestRenderNormalizesTabsForWrapMode(t *testing.T) {
	if got := Render("a\tb", 0, false); got != "a    b" {
		t.Fatalf("unwrapped tab = %q, want four spaces", got)
	}
	if got := Render("a\tb", 20, false); got != "a b" {
		t.Fatalf("wrapped tab = %q, want one space", got)
	}
}

func TestRenderKeepsIntrawordEmphasisMarkersLiteral(t *testing.T) {
	for _, text := range []string{"compile_commands.json symlink", "foo_bar_baz", "5*3*7", "compile**name**rest"} {
		if got := Render(text, 0, true); got != text {
			t.Errorf("Render(%q) = %q, want literal input", text, got)
		}
	}
}

func TestRenderStylesBareURLsAndLeavesPunctuationOutside(t *testing.T) {
	got := Render("see HTTPS://example.com/docs. now", 0, true)
	want := "see \x1b[4mHTTPS://example.com/docs\x1b[24m. now"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderWrapsBareLinksWithoutStylingTheBreakIndent(t *testing.T) {
	got := Render("- word https://x.com", 6, true)
	want := "\x1b[2m• \x1b[22mword\x1b[4m\x1b[24m\n  \x1b[4mhttps://x.com\x1b[24m"
	if got != want {
		t.Fatalf("wrapped list link = %q, want %q", got, want)
	}
}

func TestHasTrailingNewlineIgnoresSGRSequences(t *testing.T) {
	if !HasTrailingNewline("\x1b[2mcode\n\x1b[22m") {
		t.Fatal("styled code block should count its visible trailing newline")
	}
	if HasTrailingNewline("text\x1b[22m") {
		t.Fatal("style reset must not create a visible trailing newline")
	}
}

func TestRenderAlignsGFMTableAndFormatsCells(t *testing.T) {
	input := "| Col | Note |\n|---|---|\n| a | **bold** x |\n| bb | `code` |"
	want := "\x1b[1mCol\x1b[22m \x1b[2m│\x1b[22m \x1b[1mNote\x1b[22m\n" +
		"\x1b[2m────┼───────\x1b[22m\n" +
		"a   \x1b[2m│\x1b[22m \x1b[1mbold\x1b[22m x\n" +
		"bb  \x1b[2m│\x1b[22m \x1b[36mcode\x1b[39m\n"
	if got := Render(input, 40, true); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderReflowsWideTablesIntoLabeledRows(t *testing.T) {
	input := "| Component | Role | Owner |\n|---|---|---|\n| parser | reads tokens | ann |\n| writer | emits bytes | bob |"
	want := "\x1b[2m• \x1b[22m\x1b[1mComponent\x1b[22m: parser\n" +
		"  \x1b[1mRole\x1b[22m: reads tokens\n" +
		"  \x1b[1mOwner\x1b[22m: ann\n" +
		"\x1b[2m• \x1b[22m\x1b[1mComponent\x1b[22m: writer\n" +
		"  \x1b[1mRole\x1b[22m: emits bytes\n" +
		"  \x1b[1mOwner\x1b[22m: bob\n"
	if got := Render(input, 20, true); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderFallsBackWhenPipeContinuationHasExtraCells(t *testing.T) {
	input := "| H |\n|---|\n| x |\na | b"
	want := input
	if got := Render(input, 40, true); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderAcceptsBodyRowsWithoutOuterPipes(t *testing.T) {
	input := "| A | B |\n|---|---|\none | two"
	want := "\x1b[1mA\x1b[22m   [2m│\x1b[22m \x1b[1mB\x1b[22m\n" +
		"\x1b[2m────┼────[22m\n" +
		"one [2m│\x1b[22m two\n"
	if got := Render(input, 40, true); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderAlignsGFMTableColumns(t *testing.T) {
	input := "| L | CC | RR |\n|:---|:---:|---:|\n| x | y | z |"
	want := "[1mL[22m [2m│[22m [1mCC[22m [2m│[22m [1mRR[22m\n" +
		"[2m──┼────┼───[22m\n" +
		"x [2m│[22m y  [2m│[22m  z\n"
	if got := Render(input, 40, true); got != want {
		t.Fatalf("aligned table = %q, want %q", got, want)
	}
}

func TestRenderAcceptsOneDashTableDelimiter(t *testing.T) {
	input := "| H |\n|-|\n| x |"
	want := "\x1b[1mH\x1b[22m\n\x1b[2m─\x1b[22m\nx\n"
	if got := Render(input, 40, true); got != want {
		t.Fatalf("single-dash table delimiter = %q, want %q", got, want)
	}
}

func TestRenderRejectsDelimiterWithRepeatedEdgeColons(t *testing.T) {
	input := "| H |\n|::---::|\n| x |"
	if got := Render(input, 0, false); got != input {
		t.Fatalf("malformed delimiter row was formatted: %q", got)
	}
}

func TestRenderLeavesTablesWithTooManyColumnsLiteral(t *testing.T) {
	const columns = 33
	header := make([]string, columns)
	delimiter := make([]string, columns)
	for index := range columns {
		header[index] = "H"
		delimiter[index] = "---"
	}
	input := "| " + strings.Join(header, " | ") + " |\n| " + strings.Join(delimiter, " | ") + " |\n"
	if got := Render(input, 0, false); got != input {
		t.Fatalf("Render() formatted a table with %d columns: got %q", columns, got[:min(len(got), 80)])
	}
	if got := Render(input, 20, false); strings.Count(got, "\n") <= strings.Count(input, "\n") {
		t.Fatal("Render() did not wrap the over-cap table through the ordinary text wrapper")
	}
}

func TestRenderLeavesOversizedTableHeaderLiteral(t *testing.T) {
	header := "| " + strings.Repeat("H", 65536) + " |\n"
	input := header + "|---|\n| x |\n"
	if got := Render(input, 0, false); got != input {
		t.Fatalf("Render() formatted a header beyond the table byte cap")
	}
}

func TestRenderPassesOversizedBodyRowThroughAfterTable(t *testing.T) {
	row := "| " + strings.Repeat("a", 70000) + " |\n"
	input := "| H |\n|---|\n" + row
	got := Render(input, 0, false)
	if !strings.HasPrefix(got, "H\n─\n") || !strings.HasSuffix(got, row) {
		t.Fatalf("Render() did not format the bounded table then pass through its oversized row: prefix %q, suffix %q", got[:min(len(got), 24)], got[max(0, len(got)-24):])
	}
}

func TestRenderLeavesTablesBeyondRowCapLiteral(t *testing.T) {
	var input strings.Builder
	input.WriteString("| H |\n|---|\n")
	for range maxTableRows {
		input.WriteString("| x |\n")
	}
	value := input.String()
	if got := Render(value, 0, false); got != value {
		t.Fatal("Render() formatted a table after its collected row span exceeded the cap")
	}
}

func TestRenderLeavesOversizedUnterminatedTableRowLiteral(t *testing.T) {
	input := "| H |\n|---|\n| " + strings.Repeat("a", 70000) + " |"
	if got := Render(input, 0, false); got != input {
		t.Fatal("Render() formatted an oversized unterminated row instead of falling back losslessly")
	}
}

// TestRenderKeepsDeferredTailInsideOpenRun pins the end-of-input rule against the reference driver
// (checked byte-for-byte): a tail that stayed ambiguous
// during streaming is emitted literally, inside whatever inline run is still open, and that run
// closes after it, which is what markdown.c's newline handler and md_flush do.
func TestRenderKeepsDeferredTailInsideOpenRun(t *testing.T) {
	fixtures := []struct {
		input string
		width int
		want  string
	}{
		{"===- **b\u2014\r\r\n*", 0, "===- \x1b[1mb \u2014\r\r *\x1b[22m"},
		{"===- **b\u2014\r\r\n*", 20, "===- \x1b[1mb \u2014 *\x1b[22m"},
		{"1. \r\n`\n", 0, "\x1b[2m1. \x1b[22m\r `\n"},
		{"1. \r\n`\n", 20, "\x1b[2m1. \x1b[22m `\n"},
		{"abc\n`\n", 0, "abc `\n"},
		{"abc\n* ", 0, "abc\n* "},
	}
	for _, fixture := range fixtures {
		if got := Render(fixture.input, fixture.width, true); got != fixture.want {
			t.Errorf("Render(%q, %d) = %q\n  reference =%q", fixture.input, fixture.width, got, fixture.want)
		}
	}
}
