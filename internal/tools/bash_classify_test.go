package tools

import "testing"

func TestIsExplorationBashMatchesUpstreamCommandShapes(t *testing.T) {
	for _, command := range []string{
		"rg pattern | head",
		"find . -type f | sort | uniq",
		"find . -type f | xargs grep foo",
		"ls; pwd",
		"cd src && rg pattern | head -20",
		"env LC_ALL=C grep foo bar.c",
		"find . -name foo 2>/dev/null",
		"grep -r foo src 2>&1 | head",
		"git log -1 --stat && git show --stat HEAD | head -50",
		"git -C ~/source/gax log -1",
		"git --git-dir=.git status",
		"find . -name '*.c' -printf '%p\\n'",
		"wc -l foo.c",
	} {
		if !isExplorationBash(command) {
			t.Errorf("isExplorationBash(%q) = false, want true", command)
		}
	}
}

func TestIsExplorationBashRejectsWritesAndUnknownSegments(t *testing.T) {
	for _, command := range []string{
		"find . -delete",
		"find . -exec rm {} \\;",
		"find . -name '*.tmp' | xargs rm",
		"cat foo | tee out.txt",
		"sort -o out.txt in.txt",
		"sed -Ei 's/x/y/' file.c",
		"awk -i inplace '{print}' file.c",
		"git diff --output patch.diff",
		"git reflog expire --all",
		"find . -name foo && rm bar",
		"ls && cargo build",
		"rg pattern > results.txt",
		"rg \"$(touch marker)\"",
		"cargo test",
	} {
		if isExplorationBash(command) {
			t.Errorf("isExplorationBash(%q) = true, want false", command)
		}
	}
}
