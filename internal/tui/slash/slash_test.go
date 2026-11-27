package slash_test

import (
	"testing"

	"github.com/luank3/gax/internal/tui/slash"
)

func TestCompleteCommandsAndArgs(t *testing.T) {
	matches, lcp := slash.Complete("/pro", nil)
	if len(matches) != 1 || matches[0] != "/provider" || lcp != "/provider" {
		t.Fatalf("unexpected /pro completion: matches=%v lcp=%q", matches, lcp)
	}

	matches, lcp = slash.Complete("/provider open", func(cmd string) []string {
		if cmd == "/provider" {
			return []string{"openai", "openrouter", "opencode-zen"}
		}
		return nil
	})
	if len(matches) != 3 || lcp != "/provider open" {
		t.Fatalf("unexpected arg completion: matches=%v lcp=%q", matches, lcp)
	}
}
