package input

import (
	"bytes"
	"strings"
	"testing"
)

func TestHistorySearchPromptUsesASCIIWhenUTF8IsUnavailable(t *testing.T) {
	got := buildHistorySearchPromptForLocale("needle", -1, false, 80, false)
	if got != "reverse-search : needle > " {
		t.Fatalf("ASCII history search prompt = %q", got)
	}
	clipped := clipDisplayTail("a long history query", 6, false)
	if clipped != "<query" {
		t.Fatalf("ASCII clipped query = %q, want left marker and query tail", clipped)
	}
}

func TestClipIndicatorUsesASCIIWhenUTF8IsUnavailable(t *testing.T) {
	var frame bytes.Buffer
	appendClipIndicatorForLocale(&frame, 3, 80, false)
	if got := frame.String(); !strings.Contains(got, "... +3 lines") || strings.Contains(got, "…") {
		t.Fatalf("ASCII clip indicator = %q", got)
	}
}
