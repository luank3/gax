package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luank3/gax/internal/agent"
)

func TestExtractPromptImages(t *testing.T) {
	dir := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	imgPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imgPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	txtPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := agent.ExtractPromptImages(dir, "look at @shot.png and @notes.txt and @shot.png")
	if len(got) != 1 || got[0].MediaType != "image/png" || got[0].Data == "" {
		t.Fatalf("unexpected extracted images: %+v", got)
	}
}
