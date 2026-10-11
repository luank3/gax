package tools

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestUnifiedDiffUsesGNUHunkRangesAndHandlesMissingFinalNewline(t *testing.T) {
	got := unifiedDiff([]byte("a\nb"), []byte("a\nb\n"), "a/foo", "b/foo")
	want := "--- a/foo\n" +
		"+++ b/foo\n" +
		"@@ -1,2 +1,2 @@\n" +
		" a\n" +
		"-b\n\\ No newline at end of file\n" +
		"+b\n"
	if got != want {
		t.Fatalf("diff mismatch:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestUnifiedDiffUsesZeroCountRangesForInsertAndDelete(t *testing.T) {
	for _, test := range []struct {
		old, new, header string
	}{
		{old: "", new: "alpha\n", header: "@@ -0,0 +1 @@\n"},
		{old: "alpha\n", new: "", header: "@@ -1 +0,0 @@\n"},
	} {
		got := unifiedDiff([]byte(test.old), []byte(test.new), "a/f", "b/f")
		if !strings.Contains(got, test.header) {
			t.Errorf("diff %q -> %q = %q, want header %q", test.old, test.new, got, test.header)
		}
	}
}

func TestUnifiedDiffSanitizesBinaryAndInvalidUTF8Bytes(t *testing.T) {
	old := []byte{'a', 0, 0xff, '\n'}
	got := unifiedDiff(old, []byte("abc\n"), "a/f", "b/f")
	if strings.ContainsRune(got, '\x00') || strings.ContainsRune(got, '\ufffd') == false {
		t.Fatalf("diff did not sanitize binary input: %q", got)
	}
}

func TestUnifiedDiffAlignsInsertedBlocksAroundBlankLines(t *testing.T) {
	old := "void a()\n{\n}\n\nvoid c()\n{\n}\n"
	new := "void a()\n{\n}\n\nvoid b()\n{\n}\n\nvoid c()\n{\n}\n"
	got := unifiedDiff([]byte(old), []byte(new), "a/f", "b/f")
	want := "--- a/f\n" +
		"+++ b/f\n" +
		"@@ -2,6 +2,10 @@\n" +
		" {\n" +
		" }\n" +
		" \n" +
		"+void b()\n" +
		"+{\n" +
		"+}\n" +
		"+\n" +
		" void c()\n" +
		" {\n" +
		" }\n"
	if got != want {
		t.Fatalf("diff mismatch:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestUnifiedDiffKeepsSparseEditsLocalInLargeFiles(t *testing.T) {
	const lineCount, editStride = 40000, 2000
	var oldText, newText strings.Builder
	oldText.Grow(lineCount * 12)
	newText.Grow(lineCount * 12)
	for line := 0; line < lineCount; line++ {
		fmt.Fprintf(&oldText, "l%d\n", line)
		if line%editStride == 500 {
			fmt.Fprintf(&newText, "e%d\n", line)
		} else {
			fmt.Fprintf(&newText, "l%d\n", line)
		}
	}
	got := unifiedDiff([]byte(oldText.String()), []byte(newText.String()), "a/f", "b/f")
	if count := strings.Count(got, "@@ -"); count != lineCount/editStride {
		t.Fatalf("diff has %d hunks, want %d", count, lineCount/editStride)
	}
	for _, expected := range []string{"@@ -498,7 +498,7 @@\n", "-l500\n", "+e500\n", "-l38500\n", "+e38500\n"} {
		if !strings.Contains(got, expected) {
			t.Errorf("diff does not contain %q", expected)
		}
	}
	if len(got) >= 4096 {
		t.Fatalf("sparse diff expanded to %d bytes", len(got))
	}
}

func TestBoundedMyersKeepsUnchangedLinesPairedForRandomInputs(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for iteration := 0; iteration < 1000; iteration++ {
		oldSide := randomDiffSide(random)
		newSide := randomDiffSide(random)
		context := newRandomDiffContext(oldSide, newSide)
		context.diffRegion(0, len(oldSide.hashes), 0, len(newSide.hashes))
		context.slideRuns(oldSide, context.oldChanged)
		context.slideRuns(newSide, context.newChanged)
		oldLine, newLine := 0, 0
		for oldLine < len(context.oldChanged) || newLine < len(context.newChanged) {
			oldChanged := oldLine < len(context.oldChanged) && context.oldChanged[oldLine]
			newChanged := newLine < len(context.newChanged) && context.newChanged[newLine]
			if oldChanged || newChanged {
				for oldLine < len(context.oldChanged) && context.oldChanged[oldLine] {
					oldLine++
				}
				for newLine < len(context.newChanged) && context.newChanged[newLine] {
					newLine++
				}
				continue
			}
			if oldLine >= len(oldSide.hashes) || newLine >= len(newSide.hashes) || !oldSide.equal(oldLine, newSide, newLine) {
				t.Fatalf("iteration %d has unpaired unchanged lines at old=%d new=%d", iteration, oldLine, newLine)
			}
			oldLine++
			newLine++
		}
	}
}

func randomDiffSide(random *rand.Rand) diffSide {
	var data strings.Builder
	for count := random.Intn(10); count > 0; count-- {
		fmt.Fprintf(&data, "line-%d\n", random.Intn(5))
	}
	return newDiffSide([]byte(data.String()))
}

func newRandomDiffContext(oldSide, newSide diffSide) diffContext {
	return diffContext{
		oldSide: oldSide, newSide: newSide,
		oldChanged: make([]bool, len(oldSide.hashes)), newChanged: make([]bool, len(newSide.hashes)),
		forward: make([]int, 2*(2*diffRegionMax+3)), work: diffWorkLimit,
		backward: make([]int, 2*diffRegionMax+3), vectorOffset: diffRegionMax + 1,
	}
}
