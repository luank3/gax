package subagent_test

import (
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
}
