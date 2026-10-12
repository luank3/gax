package bashcompress

import (
	"strings"
	"testing"
)

func TestParseAcceptsSimpleCommands(t *testing.T) {
	cases := []struct {
		command string
		program string
		args    []string
	}{
		{"ls -la", "ls", []string{"-la"}},
		{"git status", "git", []string{"status"}},
		{"/usr/bin/git diff HEAD", "git", []string{"diff", "HEAD"}},
		{"  grep -rn 'foo bar' .  ", "grep", []string{"-rn", "foo bar", "."}},
		{"FOO=1 BAR=2 cargo test", "cargo", []string{"test"}},
		{"sudo systemctl status", "systemctl", []string{"status"}},
		{"env FOO=bar rg pattern", "rg", []string{"pattern"}},
		{`grep "a|b" file`, "grep", []string{"a|b", "file"}},
		{`echo 'semi;colon'`, "echo", []string{"semi;colon"}},
	}
	for _, testCase := range cases {
		invocation, ok := Parse(testCase.command)
		if !ok {
			t.Errorf("Parse(%q) was refused", testCase.command)
			continue
		}
		if invocation.Program != testCase.program {
			t.Errorf("Parse(%q).Program = %q, want %q", testCase.command, invocation.Program, testCase.program)
		}
		if strings.Join(invocation.Args, "\x00") != strings.Join(testCase.args, "\x00") {
			t.Errorf("Parse(%q).Args = %q, want %q", testCase.command, invocation.Args, testCase.args)
		}
	}
}

func TestParseRefusesEverythingButOneSimpleCommand(t *testing.T) {
	for _, command := range []string{
		"",
		"   ",
		"ls | head",
		"ls > out.txt",
		"ls >> out.txt",
		"cat < in.txt",
		"ls; rm -rf /",
		"ls && git status",
		"ls || true",
		"sleep 1 &",
		"echo `git status`",
		"echo $(git status)",
		"git status\nls",
		"grep 'unterminated",
		"FOO=1",
	} {
		if _, ok := Parse(command); ok {
			t.Errorf("Parse(%q) was accepted", command)
		}
	}
}

func TestCompressDispatchesByProgramAndSubcommand(t *testing.T) {
	status := "On branch main\nnothing to commit, working tree clean\n"
	if _, ok := Compress("git status", status, 0); !ok {
		t.Fatal("git status was not compressed")
	}
	if _, ok := Compress("git stash list", "stash@{0}: WIP on main\n", 0); ok {
		t.Fatal("an unrelated git subcommand was compressed")
	}
	if _, ok := Compress("ls -l", status, 0); ok {
		t.Fatal("an unrelated program's output was compressed")
	}
}

func TestCompressRefusesWhenTheCompactFormIsNotSmaller(t *testing.T) {
	// The filter's own summary is longer than the raw output, so the raw bytes stay.
	raw := "On branch main\nnothing to commit, working tree clean\n"
	compressed, ok := Compress("git status", raw, 0)
	if !ok {
		t.Fatal("git status was not compressed")
	}
	if len(compressed.Text) >= len(raw) {
		t.Fatalf("compressed form is not smaller: %q", compressed.Text)
	}
}

func TestCompressRefusesPipelinesAndRedirects(t *testing.T) {
	status := "On branch main\nnothing to commit, working tree clean\n"
	for _, command := range []string{"git status | head -2", "git status > out", "git status; ls"} {
		if _, ok := Compress(command, status, 0); ok {
			t.Errorf("%q was compressed", command)
		}
	}
}

const lsLongFixture = `total 5724
drwxr-xr-x  9 dat dat    4096 Oct 12 01:49 .
drwxr-xr-x 21 dat dat    4096 Oct 12 01:03 ..
-rw-r--r--  1 dat dat     267 Oct 11 11:36 .editorconfig
drwxr-xr-x  7 dat dat    4096 Oct 12 02:31 .git
drwxr-xr-x  3 dat dat    4096 Oct 11 11:36 .github
-rw-r--r--  1 dat dat     135 Oct 11 11:36 .gitignore
-rw-r--r--  1 dat dat  129181 Oct 12 01:23 CHANGELOG.md
drwxr-xr-x  2 dat dat    4096 Oct 11 11:36 cmd
drwxr-xr-x 32 dat dat    4096 Oct 11 11:36 internal
drwxr-xr-x  2 dat dat    4096 Oct 12 01:49 bin
lrwxrwxrwx  1 dat dat       3 Oct 11 11:36 latest -> bin
`

func TestFilterLsCompactsLongListings(t *testing.T) {
	result, ok := Compress("ls -l", lsLongFixture, 0)
	if !ok {
		t.Fatal("ls -l was not compressed")
	}
	want := strings.Join([]string{
		"755  .github/",
		"755  cmd/",
		"755  internal/",
		"755  bin/",
		"644  .editorconfig  267B",
		"644  .gitignore  135B",
		"644  CHANGELOG.md  126.2K",
		"777  latest -> bin  3B",
		"... (1 filtered)",
	}, "\n")
	if result.Text != want {
		t.Fatalf("ls -l = %q, want %q", result.Text, want)
	}
	if !result.Omitted {
		t.Fatal("filtered entries were not reported as omitted")
	}
}

func TestFilterLsKeepsNoiseDirectoriesWithAll(t *testing.T) {
	result, ok := Compress("ls -la", lsLongFixture, 0)
	if !ok {
		t.Fatal("ls -la was not compressed")
	}
	if !strings.Contains(result.Text, ".git/") {
		t.Fatalf("ls -la dropped a directory the user asked for: %q", result.Text)
	}
	if strings.Contains(result.Text, "dat") || strings.Contains(result.Text, "Oct 11") {
		t.Fatalf("ls -la kept owner or date columns: %q", result.Text)
	}
	// The dropped columns are what make the compact form lossy.
	if !result.Omitted {
		t.Fatal("ls -la drops the owner, group, and date columns but did not report omissions")
	}
}

func TestFilterLsRefusesShapesItCannotRead(t *testing.T) {
	for _, output := range []string{
		"a.txt\nb.txt\nc.txt\n",               // plain ls
		"internal:\na.txt\n\ncmd:\nb.txt\n",   // ls -R
		"total 4\n-rw-r--r-- 1 u g 3 x y z\n", // a locale column set it does not know
	} {
		if _, ok := Compress("ls -l", output, 0); ok {
			t.Errorf("ls -l compacted an unreadable listing: %q", output)
		}
	}
}

func TestFilterLsReportsAnEmptyDirectory(t *testing.T) {
	result, ok := Compress("ls -la", "total 8\ndrwxr-xr-x 2 u u 4096 Oct 12 01:00 .\ndrwxr-xr-x 3 u u 4096 Oct 12 01:00 ..\n", 0)
	if !ok {
		t.Fatal("an empty listing was not compacted")
	}
	if result.Text != "(empty)" {
		t.Fatalf("empty listing = %q", result.Text)
	}
}

func TestFilterTreeDropsNoiseSubtreesAndKeepsCounts(t *testing.T) {
	output := strings.Join([]string{
		".",
		"├── cmd",
		"│   └── gax",
		"│       └── main.go",
		"├── internal",
		"│   ├── app",
		"│   │   ├── app.go",
		"│   │   ├── app_test.go",
		"│   │   ├── help.go",
		"│   │   └── repl.go",
		"│   ├── agent",
		"│   │   ├── loop.go",
		"│   │   └── turn.go",
		"│   └── node_modules",
		"│       ├── left-pad",
		"│       │   ├── index.js",
		"│       │   ├── package.json",
		"│       │   └── README.md",
		"│       └── typescript",
		"│           └── lib.js",
		"└── README.md",
		"",
		"10 directories, 12 files",
	}, "\n")
	result, ok := Compress("tree", output, 0)
	if !ok {
		t.Fatal("tree was not compressed")
	}
	if !strings.HasPrefix(result.Text, "10 directories, 12 files\n") {
		t.Fatalf("tree dropped its counts line: %q", result.Text)
	}
	if strings.Contains(result.Text, "node_modules") || strings.Contains(result.Text, "left-pad") {
		t.Fatalf("tree kept a noise subtree: %q", result.Text)
	}
	if !strings.Contains(result.Text, "│   ├── app") {
		t.Fatalf("tree dropped a real subtree: %q", result.Text)
	}
	if !result.Omitted {
		t.Fatal("dropped noise entries were not reported")
	}
}

func TestFilterCatTrimsCommentsFromLargeFiles(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("package main\n\n")
	for index := 0; index < 300; index++ {
		builder.WriteString("// comment line\n")
		builder.WriteString("func f" + strings.Repeat("x", index%7) + "() {}\n\n")
	}
	result, ok := Compress("cat main.go", builder.String(), 0)
	if !ok {
		t.Fatal("cat was not compressed")
	}
	if strings.Contains(result.Text, "// comment line") {
		t.Fatalf("cat kept comment-only lines: %q", result.Text[:80])
	}
	if !strings.Contains(result.Text, "of") || !strings.Contains(result.Text, "lines; comments and blank lines trimmed]") {
		t.Fatalf("cat did not report what it trimmed: %q", result.Text[len(result.Text)-120:])
	}
	if !result.Omitted {
		t.Fatal("cat did not report omitted lines")
	}
}

func TestFilterCatKeepsSmallFilesAndFailures(t *testing.T) {
	small := "package main\n\n// a comment\nfunc main() {}\n"
	if _, ok := Compress("cat main.go", small, 0); ok {
		t.Fatal("a small file was compacted")
	}
	large := strings.Repeat("// comment\n", 400)
	if _, ok := Compress("cat missing.go", large, 1); ok {
		t.Fatal("a failed cat was compacted")
	}
	if _, ok := Compress("cat -n main.go", large, 0); ok {
		t.Fatal("a numbered dump was compacted")
	}
	if _, ok := Compress("cat a.go b.go", large, 0); ok {
		t.Fatal("a multi-file dump was compacted")
	}
	if _, ok := Compress("cat notes.unknown", large, 0); ok {
		t.Fatal("a file whose comments the filter cannot read was compacted")
	}
}

func TestFilterSearchKeepsSmallResultsAndTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", 200)
	output := "internal/app/app.go:10:func run() {\ninternal/app/app.go:11:" + long + "\n"
	result, ok := Compress("grep -rn run .", output, 0)
	if !ok {
		t.Fatal("grep output was not compressed")
	}
	if !strings.HasPrefix(result.Text, "internal/app/app.go:10:func run() {\n") {
		t.Fatalf("grep reordered matches: %q", result.Text)
	}
	if strings.Contains(result.Text, long) {
		t.Fatalf("grep kept an over-long line: %q", result.Text)
	}
	if !result.Omitted {
		t.Fatal("a truncated line was not reported as omitted")
	}
}

func TestFilterSearchGroupsPastTheCaps(t *testing.T) {
	var builder strings.Builder
	for file := 0; file < 3; file++ {
		for line := 1; line <= 30; line++ {
			builder.WriteString("pkg/file" + string(rune('a'+file)) + ".go:")
			builder.WriteString(itoaTest(line) + ":")
			builder.WriteString(strings.Repeat("match ", 20))
			builder.WriteString("\n")
		}
	}
	result, ok := Compress("rg match", builder.String(), 0)
	if !ok {
		t.Fatal("rg output was not compressed")
	}
	if !strings.HasPrefix(result.Text, "90 matches in 3 files:\n\n") {
		t.Fatalf("rg did not group by file: %q", result.Text[:60])
	}
	if !strings.Contains(result.Text, "+5 more in pkg/filea.go") {
		t.Fatalf("rg did not report per-file overflow: %q", result.Text)
	}
	if !result.Omitted {
		t.Fatal("grouped output did not report omissions")
	}
}

func TestFilterSearchRefusesShapesItCannotRead(t *testing.T) {
	for _, output := range []string{
		"some heading\nnot a match line\n",
		"rg: no such file or directory (os error 2)\n",
	} {
		if _, ok := Compress("rg pattern", output, 0); ok {
			t.Errorf("rg compacted unreadable output: %q", output)
		}
	}
}

func TestFilterAstGrepGroupsByFile(t *testing.T) {
	var builder strings.Builder
	for line := 1; line <= 8; line++ {
		builder.WriteString("src/a.rs:" + itoaTest(line) + ":let x = 1;\n")
	}
	result, ok := Compress("ast-grep run -p 'let $X'", builder.String(), 0)
	if !ok {
		t.Fatal("ast-grep output was not compressed")
	}
	if !strings.Contains(result.Text, "… 3 more match line(s) in src/a.rs") {
		t.Fatalf("ast-grep did not report the per-file cap: %q", result.Text)
	}
	if strings.Count(result.Text, "let x = 1;") != astGrepPerFile {
		t.Fatalf("ast-grep kept %d lines, want %d: %q", strings.Count(result.Text, "let x = 1;"), astGrepPerFile, result.Text)
	}
}

func TestFilterAstGrepRefusesDiagnostics(t *testing.T) {
	output := "warning[no-console]: Unexpected console statement\n  ┌─ src/a.js:2:3\n"
	if _, ok := Compress("ast-grep scan", output, 0); ok {
		t.Fatal("an ast-grep diagnostic was compacted")
	}
}

// itoaTest formats a line number for fixtures.
func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
