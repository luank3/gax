package bashcompress_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/bashcompress"
)

func TestCompressFiltersAndNeverWorse(t *testing.T) {
	// 1. Passing go test output collapses to summary
	var goOut strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&goOut, "ok  \tgithub.com/luank3/gax/pkg%d\t0.01s\n", i)
	}
	res, ok := bashcompress.Compress("go test ./...", goOut.String(), 0)
	if !ok || !res.Omitted || len(res.Text) >= goOut.Len() {
		t.Fatalf("expected compressed go test summary, got ok=%v res=%+v", ok, res)
	}

	// 2. Failing pytest keeps FAIL lines
	var pyOut strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&pyOut, "tests/test_%d.py::test_ok PASSED\n", i)
	}
	pyOut.WriteString("FAILED tests/test_auth.py::test_token - AssertionError\n")
	res, ok = bashcompress.Compress("pytest", pyOut.String(), 1)
	if !ok || !strings.Contains(res.Text, "FAILED tests/test_auth.py") || len(res.Text) >= pyOut.Len() {
		t.Fatalf("expected compressed pytest failure, got ok=%v res=%+v", ok, res)
	}

	// 3. Pipelines are left verbatim
	if _, ok := bashcompress.Compress("ls -la | grep foo", strings.Repeat("line\n", 100), 0); ok {
		t.Fatal("expected pipeline not to compress")
	}

	// 4. Short output never triggers compression (never-worse)
	if _, ok := bashcompress.Compress("ls", "a\nb\n", 0); ok {
		t.Fatal("expected short ls not to compress")
	}
}
