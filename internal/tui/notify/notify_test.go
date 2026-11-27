package notify_test

import (
	"bytes"
	"testing"

	"github.com/luank3/gax/internal/tui/notify"
	"github.com/luank3/gax/internal/tui/width"
)

func TestProgramStatusAndAttention(t *testing.T) {
	var buf bytes.Buffer
	notify.EmitProgramStatus(&buf, "auto", notify.StateWorking, true)
	if buf.String() != "\x1b]7501;working\a" {
		t.Fatalf("unexpected OSC 7501: %q", buf.String())
	}

	buf.Reset()
	notify.EmitAttention(&buf, "auto", true, "done", true)
	if buf.Len() != 0 {
		t.Fatalf("focused auto should not ring BEL, got %q", buf.String())
	}

	notify.EmitAttention(&buf, "auto", false, "done", true)
	if buf.String() != "\a" {
		t.Fatalf("unfocused auto should ring BEL, got %q", buf.String())
	}

	buf.Reset()
	notify.EmitAttention(&buf, "osc9", false, "all tests\npassed", true)
	if buf.String() != "\x1b]9;gax: all tests passed\a" {
		t.Fatalf("unexpected OSC 9: %q", buf.String())
	}
}

func TestWidthAndResolveVT(t *testing.T) {
	if w := width.StringWidth("\x1b[36mhello\x1b[0m"); w != 5 {
		t.Fatalf("expected width 5, got %d", w)
	}
	if got := width.ResolveVT("a\nb\x1b[1A\x1b[Kc"); got != "c\nb" {
		t.Fatalf("unexpected ResolveVT: %q", got)
	}
}
