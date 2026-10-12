package live

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	terminaltheme "github.com/luank3/gax/internal/terminal/theme"
	coretools "github.com/luank3/gax/internal/tools"
)

func TestToolPreviewStreamsSanitizedRowsAndClosesGutter(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	if got := renderer.Feed([]byte("  \nalpha\tbeta\n\x1b[31mred\x1b[0m\n")); got != "┌ alpha    beta" {
		t.Fatalf("Feed() = %q, want first settled row only", got)
	}
	if got := renderer.Finalize(); got != "\n│ red\r└\n" {
		t.Fatalf("Finalize() = %q, want final row and closing gutter", got)
	}
	if got := renderer.Finalize(); got != "" {
		t.Fatalf("second Finalize() = %q, want empty", got)
	}
}

func TestToolPreviewKeepsANSIAndUTF8StateAcrossChunks(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	if got := renderer.Feed([]byte("A\x1b[")); got != "" {
		t.Fatalf("partial first chunk rendered early: %q", got)
	}
	if got := renderer.Feed([]byte("31mB\x1b]window title")); got != "" {
		t.Fatalf("partial escape sequence rendered: %q", got)
	}
	if got := renderer.Feed([]byte("\aC界\n")); got != "" {
		t.Fatalf("single pending row rendered before its successor: %q", got)
	}
	if got := renderer.Feed([]byte("next\n")); got != "┌ ABC界" {
		t.Fatalf("settled sanitized row = %q", got)
	}
	if got := renderer.Finalize(); got != "\n│ next\r└\n" {
		t.Fatalf("final sanitized row = %q", got)
	}
}

func TestToolPreviewReplacesInvalidUTF8AndTruncatesToDisplayWidth(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 12, false)
	renderer.Feed([]byte("界界界界界\xffz\n"))
	got := renderer.Finalize()
	if !strings.Contains(got, "界界界...") || strings.Contains(got, "\xff") {
		t.Fatalf("Finalize() did not sanitize and clip by cells: %q", got)
	}
}

func TestToolPreviewHeadCapElidesBlankLinesAndAddsCount(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 100, false)
	var input strings.Builder
	for line := 1; line <= 10; line++ {
		input.WriteString("line")
		input.WriteString(strconv.Itoa(line))
		input.WriteByte('\n')
		if line == 2 {
			input.WriteString(" \t\n\n")
		}
	}
	got := renderer.Feed([]byte(input.String())) + renderer.Finalize()
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line8") || strings.Contains(got, "line9") || strings.Contains(got, "line10") {
		t.Fatalf("head preview retained wrong rows: %q", got)
	}
	if !strings.Contains(got, "... (2 more lines)") {
		t.Fatalf("head preview missing exact elision count: %q", got)
	}
}

func TestToolPreviewHeadByteCapStopsBeforeTheLineLimit(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 900, false)
	var input strings.Builder
	for line := 1; line <= 8; line++ {
		input.WriteString("row")
		input.WriteString(strconv.Itoa(line))
		input.WriteString(strings.Repeat("x", 700))
		input.WriteByte('\n')
	}
	got := renderer.Feed([]byte(input.String())) + renderer.Finalize()
	if !strings.Contains(got, "row5") || strings.Contains(got, "row6") || !strings.Contains(got, "... (3 more lines)") {
		t.Fatalf("head byte cap selected wrong rows: %q", got)
	}
}

func TestToolPreviewDropsAllBlankPlainOutputAndCommitsPartialTail(t *testing.T) {
	blanks := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	blanks.Feed([]byte(" \t\n\n"))
	if got := blanks.Finalize(); got != "" {
		t.Fatalf("blank-only preview = %q, want empty", got)
	}

	partial := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	partial.Feed([]byte("partial"))
	if got := partial.Finalize(); got != "┌ partial\r›\n" {
		t.Fatalf("partial final line = %q", got)
	}
}

func TestToolPreviewHoldsNewestRowAndCommitsPartialFinalLine(t *testing.T) {
	completed := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	if got := completed.Feed([]byte("live\n")); got != "" {
		t.Fatalf("completed row emitted before it could be replaced: %q", got)
	}
	if got := completed.Finalize(); got != "┌ live\r›\n" {
		t.Fatalf("completed single row = %q", got)
	}

	partial := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	if got := partial.Feed([]byte("one\n")); got != "" {
		t.Fatalf("first pending row emitted early: %q", got)
	}
	if got := partial.Feed([]byte("two\n")); got != "┌ one" {
		t.Fatalf("next line did not settle its predecessor: %q", got)
	}
	if got := partial.Finalize(); got != "\n│ two\r└\n" {
		t.Fatalf("partial stream final row = %q", got)
	}
}

func TestToolPreviewHeadTailShowsFourNewestNonblankRows(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHeadTail, coretools.OutputPlain, 100, false)
	var input strings.Builder
	for line := 1; line <= 10; line++ {
		input.WriteString("line")
		input.WriteString(strconv.Itoa(line))
		input.WriteByte('\n')
	}
	got := renderer.Feed([]byte(input.String())) + renderer.Finalize()
	for _, want := range []string{"line1", "line2", "line3", "line4", "... (2 more lines) ...", "line7", "line8", "line9", "line10"} {
		if !strings.Contains(got, want) {
			t.Errorf("head-tail preview missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"line5", "line6"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("head-tail preview retained %q: %q", unwanted, got)
		}
	}
}

func TestToolPreviewHeadTailUsesBoundedRingAndKeepsNewestLine(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHeadTail, coretools.OutputPlain, 100, false)
	var input strings.Builder
	for line := 1; line <= 4; line++ {
		input.WriteString("head")
		input.WriteString(strconv.Itoa(line))
		input.WriteByte('\n')
	}
	for line := 5; line <= 12; line++ {
		input.WriteString("tail")
		input.WriteString(strconv.Itoa(line))
		input.WriteString(strings.Repeat("x", 400))
		input.WriteByte('\n')
	}
	got := renderer.Feed([]byte(input.String())) + renderer.Finalize()
	if !strings.Contains(got, "... (4 more lines) ...") || !strings.Contains(got, "tail12") {
		t.Fatalf("bounded tail did not keep its elision count and newest line: %q", got)
	}
	if strings.Contains(got, "tail5") || strings.Contains(got, "tail6") || strings.Contains(got, "tail7") || strings.Contains(got, "tail8") {
		t.Fatalf("bounded tail retained old suppressed rows: %q", got)
	}
}

func TestToolPreviewDiffOmitsFileHeadersAndPreservesBlankRows(t *testing.T) {
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputUnifiedDiff, 100, false)
	got := renderer.Feed([]byte("--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n\n")) + renderer.Finalize()
	if strings.Contains(got, "a/file") || strings.Contains(got, "b/file") {
		t.Fatalf("diff repeated file headers already shown in tool header: %q", got)
	}
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") || !strings.Contains(got, "@@ -1 +1 @@") {
		t.Fatalf("diff rows missing: %q", got)
	}
	if strings.Count(got, "\n") < 4 {
		t.Fatalf("diff blank row was elided: %q", got)
	}
}

func TestToolPreviewHeadTailMatchesModestOverflowAndBlankTail(t *testing.T) {
	modest := newToolPreviewRenderer(coretools.PreviewHeadTail, coretools.OutputPlain, 100, false)
	got := modest.Feed([]byte("a\nb\nc\nd\ne\n")) + modest.Finalize()
	if want := "┌ a\n│ b\n│ c\n│ d\n│ e\r└\n"; got != want {
		t.Fatalf("modest head-tail overflow = %q, want %q", got, want)
	}

	var input strings.Builder
	for line := 0; line < 4; line++ {
		input.WriteString("head")
		input.WriteString(strconv.Itoa(line))
		input.WriteByte('\n')
	}
	for line := 0; line < 8; line++ {
		input.WriteString("tail")
		input.WriteString(strconv.Itoa(line))
		input.WriteByte('\n')
	}
	input.WriteString("\n \t\n")
	renderer := newToolPreviewRenderer(coretools.PreviewHeadTail, coretools.OutputPlain, 100, false)
	got = renderer.Feed([]byte(input.String())) + renderer.Finalize()
	if !strings.Contains(got, "... (4 more lines) ...") || !strings.Contains(got, "tail7") || strings.Contains(got, "more byte") {
		t.Fatalf("blank tail rows changed visible count: %q", got)
	}
}

func TestToolPreviewHeadSuppressesBlankFooterAndCountsPartialTail(t *testing.T) {
	var lines strings.Builder
	for index := 0; index < 8; index++ {
		lines.WriteString("L")
		lines.WriteString(strconv.Itoa(index))
		lines.WriteByte('\n')
	}
	blankTail := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 100, false)
	got := blankTail.Feed([]byte(lines.String()+"\n\n\n")) + blankTail.Finalize()
	if strings.Contains(got, "more line") {
		t.Fatalf("blank rows added a phantom footer: %q", got)
	}

	partialTail := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 100, false)
	got = partialTail.Feed([]byte(lines.String()+"TAIL")) + partialTail.Finalize()
	if !strings.Contains(got, "L7") || strings.Contains(got, "TAIL") || !strings.Contains(got, "1 more line") || strings.Contains(got, "more byte") {
		t.Fatalf("partial over-cap line was not counted once: %q", got)
	}
}

func TestToolPreviewBoundsLongLinesAndTruncatesWithoutSplittingUTF8(t *testing.T) {
	long := strings.Repeat("A", 200) + "TAIL_MARKER\n"
	truncated := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 12, false)
	got := truncated.Feed([]byte(long)) + truncated.Finalize()
	if !strings.Contains(got, "AAAAAA...") || strings.Contains(got, "TAIL_MARKER") {
		t.Fatalf("long line truncation = %q", got)
	}

	bounded := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	bounded.Feed([]byte(strings.Repeat("X", 65536)))
	if len(bounded.line) > toolLineBufferCap {
		t.Fatalf("unterminated line retained %d bytes, cap is %d", len(bounded.line), toolLineBufferCap)
	}
	bounded.Finalize()

	unicodeLine := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 12, false)
	got = unicodeLine.Feed([]byte(strings.Repeat("界", 5)+"\n")) + unicodeLine.Finalize()
	if strings.Contains(got, "\xef\xbf\xbd") || !utf8.ValidString(got) {
		t.Fatalf("truncation split a UTF-8 character: %q", got)
	}
}

func TestToolPreviewDiffKeepsHunkContentAndInterHunkColor(t *testing.T) {
	input := "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-old\n context\n+new\n@@ -9 +9 @@\n--- removed dashes\n+++ added pluses\n"
	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputUnifiedDiff, 100, true)
	got := renderer.Feed([]byte(input)) + renderer.Finalize()
	for _, want := range []string{
		ansiDim + "@@ -1,2 +1,2 @@" + ansiReset,
		terminaltheme.Open(terminaltheme.Remove) + "-old" + ansiReset,
		ansiDim + " context" + ansiReset,
		terminaltheme.Open(terminaltheme.Add) + "+new" + ansiReset,
		ansiDim + "@@ -9 +9 @@" + ansiReset,
		terminaltheme.Open(terminaltheme.Remove) + "--- removed dashes" + ansiReset,
		terminaltheme.Open(terminaltheme.Add) + "+++ added pluses" + ansiReset,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff output missing %q: %q", want, got)
		}
	}
}

func TestToolPreviewDiffUsesConfiguredThemeRoles(t *testing.T) {
	previous := terminaltheme.Current()
	terminaltheme.Configure("dark", "teal")
	defer terminaltheme.Configure(previous.Name(), previous.TintName())

	renderer := newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputUnifiedDiff, 100, true)
	got := renderer.Feed([]byte("--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n")) + renderer.Finalize()
	for _, want := range []string{
		terminaltheme.Open(terminaltheme.ChromeDim) + "┌",
		ansiDim + "@@ -1 +1 @@" + ansiReset,
		terminaltheme.Open(terminaltheme.Remove) + "-old" + ansiReset,
		terminaltheme.Open(terminaltheme.Add) + "+new" + ansiReset,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("themed diff preview missing %q: %q", want, got)
		}
	}
}

func TestToolPreviewControlAndUTF8StateIsFeedPartitionInvariant(t *testing.T) {
	input := []byte("pre\x1b[31mRED\x1b[0m\x1b]0;title\a mid\x1bPpayload\x1b\\\f\xe7\x95\x8c\nlast")
	newRenderer := func() *toolPreviewRenderer {
		return newToolPreviewRenderer(coretools.PreviewHead, coretools.OutputPlain, 80, false)
	}
	render := func(chunks [][]byte) string {
		renderer := newRenderer()
		var output strings.Builder
		for _, chunk := range chunks {
			output.WriteString(renderer.Feed(chunk))
		}
		output.WriteString(renderer.Finalize())
		return output.String()
	}
	want := "┌ preRED mid界\n│ last\r└\n"
	if got := render([][]byte{input}); got != want {
		t.Fatalf("single-feed sanitized output = %q, want %q", got, want)
	}
	for split := 0; split <= len(input); split++ {
		if got := render([][]byte{input[:split], input[split:]}); got != want {
			t.Fatalf("split at %d = %q, want %q", split, got, want)
		}
	}
	chunks := make([][]byte, len(input))
	for index := range input {
		chunks[index] = input[index : index+1]
	}
	if got := render(chunks); got != want {
		t.Fatalf("bytewise output = %q, want %q", got, want)
	}
}
