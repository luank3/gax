package subagent_test

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/subagent"
)

func TestCheckDepthAndNextChildDepth(t *testing.T) {
	if err := subagent.CheckDepth(""); err != nil {
		t.Fatalf("empty depth should pass: %v", err)
	}
	if err := subagent.CheckDepth("2"); err != nil {
		t.Fatalf("depth 2 should pass: %v", err)
	}
	if err := subagent.CheckDepth("3"); err == nil {
		t.Fatal("depth 3 should fail")
	}
	if err := subagent.CheckDepth("bad"); err == nil {
		t.Fatal("malformed depth should fail closed")
	}
	if got := subagent.NextChildDepth(""); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
	if got := subagent.NextChildDepth("bad"); got != subagent.MaxDepth {
		t.Fatalf("expected MaxDepth on bad input, got %d", got)
	}

	env := subagent.ChildEnv(
		[]string{"PATH=/bin", "GAX_SUBAGENT_DEPTH=0", "GAX_MODEL=old"},
		"1",
		map[string]string{"GAX_PROVIDER": "openai", "GAX_MODEL": "gpt-5"},
	)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GAX_SUBAGENT_DEPTH=2") ||
		!strings.Contains(joined, "GAX_PROVIDER=openai") ||
		!strings.Contains(joined, "GAX_MODEL=gpt-5") ||
		strings.Contains(joined, "GAX_MODEL=old") {
		t.Fatalf("unexpected ChildEnv:\n%s", joined)
	}
}
