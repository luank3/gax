package bashcompress

import (
	"strings"
	"testing"
)

const gitStatusFixture = `On branch main
Changes to be committed:
  (use "git restore --staged <file>..." to unstage)
	modified:   src/main.go

Changes not staged for commit:
  (use "git add <file>..." to update what will be committed)
  (use "git restore <file>..." to discard changes in working directory)
	modified:   a.txt

Untracked files:
  (use "git add <file>..." to include in what will be committed)
	note.txt
	src/b.go

`

func TestFilterGitStatusGroupsByState(t *testing.T) {
	result, ok := Compress("git status", gitStatusFixture, 0)
	if !ok {
		t.Fatal("git status was not compacted")
	}
	want := "* main\nM  src/main.go\n M a.txt\n?? note.txt\n?? src/b.go"
	if result.Text != want {
		t.Fatalf("git status = %q, want %q", result.Text, want)
	}
	if result.Omitted {
		t.Fatal("a pure reformatting reported omissions")
	}
}

func TestFilterGitStatusReportsCleanTrees(t *testing.T) {
	output := "On branch main\nYour branch is up to date with 'origin/main'.\n\nnothing to commit, working tree clean\n"
	result, ok := Compress("git status", output, 0)
	if !ok {
		t.Fatal("a clean status was not compacted")
	}
	if want := "* main\nclean — nothing to commit"; result.Text != want {
		t.Fatalf("clean status = %q, want %q", result.Text, want)
	}
}

func TestFilterGitStatusKeepsInProgressState(t *testing.T) {
	output := "On branch main\nYou are currently rebasing branch 'main' on 'abc1234'.\n\nnothing to commit, working tree clean\n"
	result, ok := Compress("git status", output, 0)
	if !ok {
		t.Fatal("an in-progress status was not compacted")
	}
	if !strings.Contains(result.Text, "rebase in progress") {
		t.Fatalf("rebase state was dropped: %q", result.Text)
	}
}

func TestFilterGitStatusLeavesErrorsAlone(t *testing.T) {
	output := "fatal: not a git repository (or any of the parent directories): .git\n"
	if _, ok := Compress("git status", output, 128); ok {
		t.Fatal("a git failure was compacted")
	}
}

const gitDiffFixture = `diff --git a/a.txt b/a.txt
index b00a0f1..0eb76bd 100644
--- a/a.txt
+++ b/a.txt
@@ -1,8 +1,8 @@
 one
-two
+TWO
 three
 four
 five
 six
 seven
-eight
+nine
diff --git a/src/main.go b/src/main.go
index 06ab7d0..38dd16d 100644
--- a/src/main.go
+++ b/src/main.go
@@ -1 +1,3 @@
 package main
+
+func main() {}
`

func TestFilterGitDiffStripsHeadersAndCountsChanges(t *testing.T) {
	result, ok := Compress("git diff", gitDiffFixture, 1)
	if !ok {
		t.Fatal("git diff was not compacted")
	}
	if strings.Contains(result.Text, "index b00a0f1") || strings.Contains(result.Text, "--- a/a.txt") {
		t.Fatalf("git diff kept file headers: %q", result.Text)
	}
	if !strings.HasPrefix(result.Text, "a.txt\n") || !strings.Contains(result.Text, "\nsrc/main.go\n") {
		t.Fatalf("git diff dropped or mangled a file header: %q", result.Text)
	}
	if !strings.Contains(result.Text, "  +2 -2") || !strings.Contains(result.Text, "  +2 -0") {
		t.Fatalf("git diff lost its per-file change counts: %q", result.Text)
	}
	if !strings.Contains(result.Text, "-two") || !strings.Contains(result.Text, "+TWO") {
		t.Fatalf("git diff dropped a change: %q", result.Text)
	}
	if !strings.Contains(result.Text, " one") {
		t.Fatalf("git diff dropped the leading context: %q", result.Text)
	}
	if result.Omitted {
		t.Fatal("a complete diff reported omissions")
	}
}

func TestFilterGitDiffTruncatesLargeHunks(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("diff --git a/big.txt b/big.txt\n@@ -1,200 +1,200 @@\n")
	for index := 0; index < 200; index++ {
		builder.WriteString("+line " + itoaTest(index) + "\n")
	}
	result, ok := Compress("git diff", builder.String(), 1)
	if !ok {
		t.Fatal("a large diff was not compacted")
	}
	if !strings.Contains(result.Text, "truncated") {
		t.Fatalf("git diff did not report the hunk cap: %q", result.Text)
	}
	if !result.Omitted {
		t.Fatal("a truncated hunk did not report omissions")
	}
}

func TestFilterGitDiffRefusesNonDiffOutput(t *testing.T) {
	stat := " internal/app/app.go | 3 +++\n 1 file changed, 3 insertions(+)\n"
	if _, ok := Compress("git diff --stat", stat, 0); ok {
		t.Fatal("a diffstat was compacted as a diff")
	}
}

const gitLogFixture = `commit 6a56b9c799c09d0a7f0fcceb561d1b05e78d94cb
Author: T <t@t>
Date:   Mon Oct 12 09:57:19 2026 +0700

    first commit
    with a body line
    and another

commit 04ac403f996fc520b2b12afc6ea44c8c4f7106b4
Author: luank3 <lua_n@naver.com>
Date:   Sun Oct 12 02:29:54 2026 +0700

    Park the status indicator below an open read row
`

func TestFilterGitLogPrintsOneLinePerCommit(t *testing.T) {
	result, ok := Compress("git log", gitLogFixture, 0)
	if !ok {
		t.Fatal("git log was not compacted")
	}
	lines := splitLines(result.Text)
	if len(lines) != 4 {
		t.Fatalf("git log = %q, want two commits of one header plus a body line", result.Text)
	}
	if !strings.HasPrefix(lines[0], "6a56b9c first commit (") {
		t.Fatalf("first commit header = %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], "<T <t@t>>") {
		t.Fatalf("first commit author = %q", lines[0])
	}
	if !strings.HasPrefix(lines[3], "04ac403 Park the status indicator") {
		t.Fatalf("second commit header = %q", lines[3])
	}
	if strings.Contains(result.Text, "commit 6a56b9c799c09d0a7f0fcceb561d1b05e78d94cb") {
		t.Fatalf("git log kept a full hash: %q", result.Text)
	}
}

func TestFilterGitLogHonorsALimit(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < 30; index++ {
		builder.WriteString("commit " + strings.Repeat("a", 39) + itoaTest(index) + "\n")
		builder.WriteString("Author: T <t@t>\n")
		builder.WriteString("Date:   Mon Oct 12 09:57:19 2026 +0700\n\n    commit " + itoaTest(index) + "\n\n")
	}
	result, ok := Compress("git log -n 2", builder.String(), 0)
	if !ok {
		t.Fatal("a limited git log was not compacted")
	}
	if got := len(splitLines(result.Text)); got != 2 {
		t.Fatalf("git log -n 2 printed %d commits: %q", got, result.Text)
	}
	if !result.Omitted {
		t.Fatal("dropped commits were not reported")
	}
}

func TestFilterGitLogRefusesPatchOutput(t *testing.T) {
	if _, ok := Compress("git log -p", gitLogFixture+"diff --git a/a b/a\n@@ -1 +1 @@\n-a\n+b\n", 0); ok {
		t.Fatal("a patch log was compacted as a listing")
	}
}

func TestFilterGitConfirmations(t *testing.T) {
	cases := []struct {
		command string
		output  string
		want    string
	}{
		{"git commit -m x", "[main abc1234def] message\n 1 file changed, 1 insertion(+)\n", "ok abc1234"},
		{"git commit -m x", "main abc1234def message\n", "ok"},
		{"git push", "To github.com:o/r.git\n   abc1234..def5678  main -> main\n", "ok main"},
		{"git push -u origin feature", "To github.com:o/r.git\n * [new branch]      feature -> feature\n", "ok feature (new)"},
		{"git push", "Everything up-to-date\n", "ok"},
		{"git pull", "Updating abc1234..def5678\nFast-forward\n a.go | 2 ++\n 1 file changed, 2 insertions(+)\n", "ok 1 files +2 -0"},
		{"git pull", "Already up to date.\n", "ok"},
		{"git stash", "Saved working directory and index state WIP on main: abc1234 msg\n", "ok stash saved"},
		{"git stash pop", "On branch main\nDropped refs/stash@{0} (abc1234)\n", "ok stash dropped"},
	}
	for _, testCase := range cases {
		result, ok := Compress(testCase.command, testCase.output, 0)
		if !ok {
			t.Errorf("%q was not compacted (output %q)", testCase.command, testCase.output)
			continue
		}
		if result.Text != testCase.want {
			t.Errorf("%q = %q, want %q", testCase.command, result.Text, testCase.want)
		}
	}
}

func TestFilterGitConfirmationsLeaveSilentCommandsAlone(t *testing.T) {
	// `git add` prints nothing on success, and "ok" would be more bytes than nothing.
	if _, ok := Compress("git add -A", "", 0); ok {
		t.Fatal("a silent git add was replaced by a longer confirmation")
	}
}

func TestFilterGitConfirmationsKeepFailures(t *testing.T) {
	for _, testCase := range []struct {
		command string
		output  string
	}{
		{"git commit -m x", "nothing to commit, working tree clean\n"},
		{"git push", "fatal: The current branch has no upstream branch.\n"},
		{"git fetch", "fatal: unable to access 'https://example.com/': Could not resolve host\n"},
	} {
		if _, ok := Compress(testCase.command, testCase.output, 1); ok {
			t.Errorf("%q compacted a failure", testCase.command)
		}
	}
}
