package markdown

import (
	"strings"
	"testing"
)

func benchRender(b *testing.B, input string) {
	b.SetBytes(int64(len(input)))
	for b.Loop() {
		_ = Render(input, 0, true)
	}
}

func BenchmarkRenderProse4K(b *testing.B)    { benchRender(b, strings.Repeat("word ", 800)) }
func BenchmarkRenderProse16K(b *testing.B)   { benchRender(b, strings.Repeat("word ", 3200)) }
func BenchmarkRenderProse64K(b *testing.B)   { benchRender(b, strings.Repeat("word ", 12800)) }
func BenchmarkRenderBadUTF84K(b *testing.B)  { benchRender(b, strings.Repeat("\xe4", 4096)) }
func BenchmarkRenderBadUTF816K(b *testing.B) { benchRender(b, strings.Repeat("\xe4", 16384)) }
func BenchmarkRenderBadUTF864K(b *testing.B) { benchRender(b, strings.Repeat("\xe4", 65536)) }

// feedInChunks measures the streaming path at a fixed chunk size, which is how a provider
// delivers an answer. Render() feeds once, so it hides the per-feed cost.
func feedInChunks(b *testing.B, input string, chunk int) {
	b.SetBytes(int64(len(input)))
	for b.Loop() {
		stream := NewStream(0, true, func(string) error { return nil })
		for offset := 0; offset < len(input); offset += chunk {
			end := offset + chunk
			if end > len(input) {
				end = len(input)
			}
			_ = stream.Feed(input[offset:end])
		}
		_ = stream.Close()
	}
}

func BenchmarkStream8KChunk64(b *testing.B)  { feedInChunks(b, strings.Repeat("word ", 1600), 64) }
func BenchmarkStream32KChunk64(b *testing.B) { feedInChunks(b, strings.Repeat("word ", 6400), 64) }
func BenchmarkStream64KChunk64(b *testing.B) { feedInChunks(b, strings.Repeat("word ", 12800), 64) }
func BenchmarkStream32KChunk8(b *testing.B)  { feedInChunks(b, strings.Repeat("word ", 6400), 8) }
func BenchmarkRender64K(b *testing.B)        { benchRender(b, strings.Repeat("word ", 12800)) }

func BenchmarkStream64KChunk1K(b *testing.B)  { feedInChunks(b, strings.Repeat("word ", 12800), 1024) }
func BenchmarkStream64KChunk8K(b *testing.B)  { feedInChunks(b, strings.Repeat("word ", 12800), 8192) }
func BenchmarkStream64KChunk64K(b *testing.B) { feedInChunks(b, strings.Repeat("word ", 12800), 65536) }
