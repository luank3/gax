package diag

import (
	"bytes"
	"testing"
)

func TestWarningsAndErrorsWriteOneLineAndAdvanceSequence(t *testing.T) {
	var output bytes.Buffer
	before := Sequence()
	Warn(&output, "warning %s", "text")
	Error(&output, "error %d", 7)
	if got, want := output.String(), "gax: warning text\ngax: error 7\n"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
	if got := Sequence(); got != before+2 {
		t.Fatalf("diagnostic sequence = %d, want %d", got, before+2)
	}
}
