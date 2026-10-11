package markdown

import (
	"math/rand"
	"strings"
	"testing"
)

// inlineCorpus is the set of inline inputs the differential gate exercises.
var inlineCorpus = []string{
	"plain prose", "**bold**", "_italic_", "`code`", "a—b", "x — y", "http://example.com/a",
	"see https://example.com/docs.", "trailing spaces  ", "line one\nline two",
	"**unclosed", "_unclosed", "`unclosed", "*", "**", "_", "`", "a*", "a**", "a_",
	"__", "___", "***", "****", "**foo*bar**", "*a_b*c_", "—", "a—", "—a", "\r", "a\r",
	"a  \nb", "a\r\nb", "ht", "htt", "https:", "https:/", "http://", "h", "H",
	"`a`b`c`", "x`y`", "*a* b *c*", "**a**b**c**", "a**b", "\\*escaped", "[link](url)",
	"text with *emphasis* and `code` and http://x.y/z and — dashes",
}

func inlineFeedOnce(t *testing.T, input string, styled bool) string {
	t.Helper()
	var output strings.Builder
	state := inlineState{styled: styled}
	if _, err := state.renderResumable(func(chunk string) error {
		output.WriteString(chunk)
		return nil
	}, input, true); err != nil {
		t.Fatalf("renderResumable(%q): %v", input, err)
	}
	return output.String()
}

func TestInlineResumableMatchesBatchAcrossChunkings(t *testing.T) {
	for _, input := range inlineCorpus {
		want, _ := renderInlineWithState(input, true, false, false)
		var output strings.Builder
		emit := func(chunk string) error {
			output.WriteString(chunk)
			return nil
		}
		state := inlineState{styled: true}
		offset, window := 0, 1
		for offset < len(input) {
			end := offset + window
			if end > len(input) {
				end = len(input)
			}
			final := end == len(input)
			consumed, err := state.renderResumable(emit, input[offset:end], final)
			if err != nil {
				t.Fatalf("renderResumable(%q): %v", input, err)
			}
			offset += consumed
			if consumed == 0 {
				if final {
					t.Fatalf("no progress at EOF for %q (offset %d)", input, offset)
				}
				window++
				continue
			}
			window = 1
		}
		if _, err := state.renderResumable(emit, "", true); err != nil {
			t.Fatalf("final renderResumable(%q): %v", input, err)
		}
		if got := output.String(); got != want {
			t.Fatalf("input=%q\n  chunked=%q\n  batch  =%q", input, got, want)
		}
	}
}

// TestInlineResumableGeneratedCorpus widens the differential gate beyond the hand-written corpus.
func TestInlineResumableGeneratedCorpus(t *testing.T) {
	alphabet := []string{
		"a", "b", " ", "\n", "-", "*", "_", "`", "**", "__", "—", "\r", "\t", "h", "t", "p", ":",
		"/", "http://", "https://", "http://x", ".", ",", ")", "(", "[", "]", "!", "0", "Z",
	}
	random := rand.New(rand.NewSource(11))
	for iteration := 0; iteration < 30000; iteration++ {
		parts := 1 + random.Intn(10)
		input := ""
		for part := 0; part < parts; part++ {
			input += alphabet[random.Intn(len(alphabet))]
		}
		for _, styled := range []bool{true, false} {
			want, _ := renderInlineWithState(input, styled, false, false)
			var output strings.Builder
			emit := func(chunk string) error {
				output.WriteString(chunk)
				return nil
			}
			state := inlineState{styled: styled}
			offset, window := 0, 1
			for offset < len(input) {
				end := offset + window
				if end > len(input) {
					end = len(input)
				}
				final := end == len(input)
				consumed, err := state.renderResumable(emit, input[offset:end], final)
				if err != nil {
					t.Fatalf("renderResumable(%q): %v", input, err)
				}
				offset += consumed
				if consumed == 0 {
					if final {
						t.Fatalf("no progress at EOF for %q (offset %d)", input, offset)
					}
					window++
					continue
				}
				window = 1
			}
			if _, err := state.renderResumable(emit, "", true); err != nil {
				t.Fatalf("final renderResumable(%q): %v", input, err)
			}
			if got := output.String(); got != want {
				t.Fatalf("styled=%v input=%q\n  chunked=%q\n  batch  =%q", styled, input, got, want)
			}
		}
	}
}
