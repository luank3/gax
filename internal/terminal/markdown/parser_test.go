package markdown

import (
	"math/rand"
	"strings"
	"testing"
)

// parserCorpus is the block-level input set the differential gate exercises.
var parserCorpus = []string{
	"", "a", "a\n", "a\nb\n", "a\n\nb\n", "a\n\n\nb",
	"# Heading\n", "# Heading\n\ntext\n", "text\n# Heading\n", "# H\n# I\n",
	"## Sub ##\n", "####### seven\n", "#nospace\n", "#\n",
	"- item\n", "- item\n- two\n", "* star\n", "+ plus\n",
	"1. one\n2. two\n", "1) paren\n", "10. ten\n",
	"- a\n  b\n", "- a\n\n- b\n", "text\n- list\n", "text\n    - four space\n",
	"> quote\n", "> quote\n> more\n", "text\n> quote\n",
	"---\n", "***\n", "===\n", "text\n===\n", "text\n---\n",
	"```\ncode\n```\n", "```go\nx := 1\n```\n", "```\nunclosed\n", "```\n", "````\n```\n````\n",
	"text\n```\ncode\n```\n", "text\n``", "text\n````", "``", "````",
	"| a | b |\n| - | - |\n", "| a | b |\n| - | - |\n| 1 | 2 |\n",
	"| a | b |\n| - | - |\n| 1 | 2 |", "| a | b |\n", "| a | b |", "| a | b |\n| - | - |",
	"| a |\n|---|\n| b |\n", "text\n| a | b |\n| - | - |\n| 1 | 2 |\n",
	"| a | b |\n| - | - |\n\n| c | d |\n| - | - |\n",
	"| a | b |\n| - | - |\nplain\n", "| a | b |\n| - | - |\nplain",
	"|not a table\n", "| a | b\n", "a | b\n",
	"**bold** text\n", "_italic_\n", "`code`\n", "text with `code`\n",
	"a—b\n", "a — b\n", "see https://example.com/x.\n",
	"trailing  \nnext\n", "trailing \nnext\n", "trailing\t\nnext\n",
	"a\r\nb\r\n", "a\r\n\r\nb\r\n", "a\r", "a\r\n", "a\n\r\nb\n",
	"  indented\n", "   \n", " \n", "\t\n", "a\n \n", "a\n  ",
	"a\n* b\n", "a\n1. b\n", "a\n- - -\n", "a\n===\n", "a\n# h\n",
	"*a\n", "# *unclosed\n", "# *unclosed", "# a\nb\n",
	"- *item*\n", "- `code`\n", "- a\n- b\n\n- c\n",
	"> *quote*\n", "| **a** | `b` |\n| - | - |\n| c | d |\n",
	"1. a\n   b\n", "1. a\n\n2. b\n",
}

// parserFeed renders input through the streaming parser, splitting it at the given sizes.
func parserFeed(input string, width int, styled bool, chunks []int) string {
	var output strings.Builder
	p := newParser(width, styled, func(text string) error {
		output.WriteString(text)
		return nil
	})
	offset := 0
	for _, size := range chunks {
		if size <= 0 {
			continue
		}
		end := offset + size
		if end > len(input) {
			end = len(input)
		}
		if offset >= end {
			break
		}
		p.feed(input[offset:end], false)
		offset = end
	}
	// The streaming shape is always a non-final feed followed by a final one, which is what
	// Stream.Feed and Stream.Close do.
	p.feed(input[offset:], false)
	p.feed("", true)
	return output.String()
}

// TestParserChunkingDoesNotChangeOutput feeds each input one byte at a time and in random splits.
// It is the gate that can fail: a whole-document render and a chunked one must agree.
func TestParserChunkingDoesNotChangeOutput(t *testing.T) {
	corpus := append(append([]string{}, inlineCorpus...), parserCorpus...)
	random := rand.New(rand.NewSource(7))
	for _, styled := range []bool{true, false} {
		for _, input := range corpus {
			want := Render(input, 24, styled)
			chunkings := [][]int{nil, {1}, {2}, {3}}
			for attempt := 0; attempt < 20; attempt++ {
				var chunks []int
				for total := 0; total < len(input)+1; total += 1 + random.Intn(5) {
					chunks = append(chunks, 1+random.Intn(5))
				}
				chunkings = append(chunkings, chunks)
			}
			for _, chunks := range chunkings {
				if got := parserFeed(input, 24, styled, chunks); got != want {
					t.Errorf("styled=%v chunks=%v input=%q\n  parser=%q\n  batch =%q",
						styled, chunks, input, got, want)
				}
			}
		}
	}
}

// TestParserCensus compares the parser against a whole-document render over generated documents,
// the same distribution the streaming census uses.
func TestParserCensus(t *testing.T) {
	alphabet := []string{
		"a", "b", "word", " ", " ", "\n", "\n\n", "- ", "* ", "1. ", "> ", "# ", "## ",
		"| ", " |", "---", "===", "```", "`", "**", "*", "_", "[", "](", ")", "http://x", "—",
		"\r", "\r\n", "\v", "\t", "  ", "0", "Z",
	}
	random := rand.New(rand.NewSource(11))
	divergent, total := 0, 0
	for iteration := 0; iteration < 4000; iteration++ {
		parts := 1 + random.Intn(8)
		var input strings.Builder
		for part := 0; part < parts; part++ {
			input.WriteString(alphabet[random.Intn(len(alphabet))])
		}
		width := random.Intn(3) * 8
		want := Render(input.String(), width, true)
		for attempt := 0; attempt < 2; attempt++ {
			cut := random.Intn(input.Len() + 1)
			got := parserFeed(input.String(), width, true, []int{cut})
			total++
			if got != want {
				divergent++
				if divergent <= 8 {
					t.Logf("input=%q cut=%d\n  parser=%q\n  batch =%q", input.String(), cut, got, want)
				}
			}
		}
	}
	t.Logf("parser divergences %d / %d", divergent, total)
	if divergent != 0 {
		t.Errorf("parser divergences %d / %d; chunking must not change the rendered bytes", divergent, total)
	}
}

// TestParserClosesFenceAtEndOfInput pins the upstream EOF branch: a valid closer with no trailing
// newline closes the fence rather than being copied into the body.
func TestParserClosesFenceAtEndOfInput(t *testing.T) {
	want := ansiDim + "foo\n" + ansiBoldOff
	if got := parserFeed("```\nfoo\n```", 0, true, nil); got != want {
		t.Errorf("parser=%q\n  want  =%q", got, want)
	}
}

// TestParserBailsOversizedPartialRowEagerly pins upstream's md_table_bail_partial: a row that alone
// exceeds the byte cap ends the attempt during the feed, emitting the collected table and the row
// literally instead of retaining them.
func TestParserBailsOversizedPartialRowEagerly(t *testing.T) {
	var output strings.Builder
	p := newParser(40, false, func(text string) error {
		output.WriteString(text)
		return nil
	})
	p.feed("| H |\n|---|\n| "+strings.Repeat("a", 70000)+" |", false)
	got := output.String()
	if !strings.HasPrefix(got, "| H |\n|---|\n|") || !strings.Contains(got, strings.Repeat("a", 100)) {
		t.Fatalf("oversized table bail lost its literal header or row body: %q", got[:min(len(got), 60)])
	}
	if p.mode == modeTable {
		t.Fatal("parser remained in table collection after the byte cap bailout")
	}
}

// TestParserFinalFeedMatchesStreamingShape pins that feeding a whole document with final set renders
// what feeding it and closing renders. The two shapes must not disagree: Stream.Feed/Close uses the
// second, and a caller rendering one document uses the first.
func TestParserFinalFeedMatchesStreamingShape(t *testing.T) {
	corpus := append(append([]string{}, inlineCorpus...), parserCorpus...)
	corpus = append(corpus, "01. `\n* ", "0_[](\n\r", " \v```Z\r\n\r", "- \t\n\v")
	for _, styled := range []bool{true, false} {
		for _, width := range []int{0, 8, 16} {
			for _, input := range corpus {
				var output strings.Builder
				p := newParser(width, styled, func(text string) error {
					output.WriteString(text)
					return nil
				})
				p.feed(input, true)
				if got, want := output.String(), Render(input, width, styled); got != want {
					t.Errorf("styled=%v width=%d input=%q\n  final feed=%q\n  feed+close=%q",
						styled, width, input, got, want)
				}
			}
		}
	}
}
