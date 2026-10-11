package markdown

import (
	"math/rand"
	"strings"
	"testing"
)

// TestStreamChunkingCensus guards that a chunked stream renders exactly what a whole-document
// render does. It is the invariant the streaming design rests on: bytes are only written once a
// later byte cannot change them, so the partition must not matter.
func TestStreamChunkingCensus(t *testing.T) {
	alphabet := []string{
		"a", "b", "word", " ", " ", "\n", "\n\n", "- ", "* ", "1. ", "> ", "# ", "## ",
		"| ", " |", "---", "===", "```", "`", "**", "*", "_", "[", "](", ")", "http://x", "—",
		"\r", "\r\n", "\v", "\t", "  ", "0", "Z",
	}
	random := rand.New(rand.NewSource(1))
	divergent, total, worst := 0, 0, 0
	for iteration := 0; iteration < 20000; iteration++ {
		parts := 1 + random.Intn(8)
		input := ""
		for part := 0; part < parts; part++ {
			input += alphabet[random.Intn(len(alphabet))]
		}
		width := random.Intn(3) * 8
		want := Render(input, width, true)
		for attempt := 0; attempt < 3; attempt++ {
			cuts := []int{random.Intn(len(input) + 1), random.Intn(len(input) + 1)}
			if cuts[0] > cuts[1] {
				cuts[0], cuts[1] = cuts[1], cuts[0]
			}
			var output strings.Builder
			stream := NewStream(width, true, func(chunk string) error { output.WriteString(chunk); return nil })
			for _, chunk := range []string{input[:cuts[0]], input[cuts[0]:cuts[1]], input[cuts[1]:]} {
				if err := stream.Feed(chunk); err != nil {
					t.Fatalf("Feed(%q): %v", input, err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("Close(%q): %v", input, err)
			}
			total++
			if output.String() != want {
				divergent++
				artifact := len(output.String()) - len(want)
				if artifact < 0 {
					artifact = -artifact
				}
				if artifact > worst {
					worst = artifact
				}
			}
		}
	}
	t.Logf("divergences %d / %d (worst artifact %d bytes)", divergent, total, worst)
	if divergent != 0 {
		t.Errorf("chunked renders differ from whole-document renders %d / %d times", divergent, total)
	}
}
