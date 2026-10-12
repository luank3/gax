package width

import (
	"strings"
	"testing"
)

func TestFlattenForDisplayMatchesSafetyRules(t *testing.T) {
	if got, want := FlattenForDisplay("  alpha\t\n beta\x01  gamma  "), "alpha beta gamma"; got != want {
		t.Fatalf("FlattenForDisplay() = %q, want %q", got, want)
	}
	if got, want := FlattenForDisplay("a\xffb\u202eX"), "a?b?X"; got != want {
		t.Fatalf("FlattenForDisplay() unsafe text = %q, want %q", got, want)
	}
	combining := "x" + strings.Repeat("\u0301", 10) + "y"
	if got, want := FlattenForDisplay(combining), "x"+strings.Repeat("\u0301", 8)+"y"; got != want {
		t.Fatalf("FlattenForDisplay() combining run = %q, want %q", got, want)
	}
}

func TestTruncateForDisplayUsesCellsAndEllipsis(t *testing.T) {
	if got, want := TruncateForDisplay("界界界", 5), "界..."; got != want {
		t.Fatalf("TruncateForDisplay() = %q, want %q", got, want)
	}
	if got, want := TruncateForDisplay("abc", 2), "ab"; got != want {
		t.Fatalf("short truncation = %q, want %q", got, want)
	}
	if got, want := TruncateForDisplay("abc", 6), "abc"; got != want {
		t.Fatalf("unneeded truncation = %q, want %q", got, want)
	}
}
