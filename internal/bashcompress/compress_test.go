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

	// 5. Make build compression preserves errors
	var makeOut strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&makeOut, "cc -O2 -c src/file%d.c -o build/file%d.o\n", i, i)
	}
	makeOut.WriteString("src/main.c:12:5: error: undeclared identifier\n")
	res, ok = bashcompress.Compress("make -j8", makeOut.String(), 2)
	if !ok || !strings.Contains(res.Text, "error: undeclared identifier") {
		t.Fatalf("expected compressed make error, got ok=%v res=%+v", ok, res)
	}

	// 6. Pip install strips download noise
	var pipOut strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&pipOut, "Collecting pkg%d\n  Downloading pkg%d-1.0.whl\n", i, i)
	}
	pipOut.WriteString("Successfully installed pkg0 pkg1\n")
	res, ok = bashcompress.Compress("pip install -r requirements.txt", pipOut.String(), 0)
	if !ok || !strings.Contains(res.Text, "Successfully installed") {
		t.Fatalf("expected compressed pip output, got ok=%v res=%+v", ok, res)
	}
}
