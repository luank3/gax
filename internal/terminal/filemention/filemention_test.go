package filemention

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMatchFindsMentionAtStartOrAfterWhitespace(t *testing.T) {
	tests := []struct {
		text   string
		cursor int
		start  int
		end    int
		ok     bool
	}{
		{text: "@foo", cursor: 4, start: 0, end: 4, ok: true},
		{text: "see @src/m", cursor: 10, start: 4, end: 10, ok: true},
		{text: "x\n@foo", cursor: 6, start: 2, end: 6, ok: true},
		{text: "@src/m x", cursor: 4, start: 0, end: 4, ok: true},
		{text: "@", cursor: 1, start: 0, end: 1, ok: true},
		{text: "email@host", cursor: 10},
		{text: "(@foo", cursor: 5},
		{text: "x @foo", cursor: 2},
	}
	for _, test := range tests {
		t.Run(test.text, func(t *testing.T) {
			start, end, ok := Match(test.text, test.cursor)
			if start != test.start || end != test.end || ok != test.ok {
				t.Fatalf("Match(%q, %d) = %d, %d, %t; want %d, %d, %t", test.text, test.cursor, start, end, ok, test.start, test.end, test.ok)
			}
		})
	}
}

func TestBuildCommandSeparatesExternalRootsAndQuotesFilters(t *testing.T) {
	tests := []struct {
		query  string
		root   string
		filter string
	}{
		{query: "src", filter: "src"},
		{query: "../a b$(x)/file", root: "../a b$(x)/", filter: "file"},
		{query: "it's", filter: "it's"},
		{query: "..", root: "../"},
		{query: "~/src/fil", root: "~/src/", filter: "fil"},
		{query: "/tmp/x", root: "/tmp/", filter: "x"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			command := BuildCommand(test.query)
			if !strings.Contains(command, "git ls-files -z --cached --others --exclude-standard") ||
				!strings.Contains(command, "|| find .") || !strings.Contains(command, "-print0") ||
				!strings.Contains(command, "--read0 --print0 --height=~40% --layout=reverse --scheme=path") {
				t.Fatalf("BuildCommand(%q) missing picker behavior: %s", test.query, command)
			}
			quotedFilter := "--query='" + strings.ReplaceAll(test.filter, "'", "'\\''") + "'"
			if !strings.Contains(command, quotedFilter) {
				t.Fatalf("BuildCommand(%q) = %s; want query %s", test.query, command, quotedFilter)
			}
			if test.root == "" && strings.Contains(command, "cd ") {
				t.Fatalf("BuildCommand(%q) unexpectedly changes directory: %s", test.query, command)
			}
			if test.root != "" && !strings.Contains(command, "cd ") {
				t.Fatalf("BuildCommand(%q) does not search its root: %s", test.query, command)
			}
		})
	}
}

func TestPickReadsNULTerminatedSelectionAndJoinsExternalRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the modal picker uses a POSIX shell")
	}
	dir := t.TempDir()
	picked := filepath.Join(dir, "picked\nfile.txt")
	if err := os.WriteFile(picked, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fzf := filepath.Join(bin, "fzf")
	if err := os.WriteFile(fzf, []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s\\0' \"$GAX_TEST_FZF_SELECTION\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GAX_TEST_FZF_SELECTION", "./picked\nfile.txt")

	got, ok, err := Pick(filepath.Join(dir, "pick"))
	if err != nil || !ok || got != picked {
		t.Fatalf("Pick() = %q, %t, %v; want %q", got, ok, err, picked)
	}
}

func TestPickInheritsTerminalInputAndErrorOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the modal picker uses a POSIX shell")
	}
	root := t.TempDir()
	picked := filepath.Join(root, "picked.txt")
	if err := os.WriteFile(picked, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	stdinCapture := filepath.Join(root, "stdin")
	git := "#!/bin/sh\n/bin/cat > \"$GAX_TEST_PICKER_STDIN\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	fzf := "#!/bin/sh\n/bin/cat >/dev/null\nprintf 'picker diagnostic\\n' >&2\nprintf '%s\\0' './picked.txt'\n"
	if err := os.WriteFile(filepath.Join(bin, "fzf"), []byte(fzf), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GAX_TEST_PICKER_STDIN", stdinCapture)

	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinWrite.WriteString("terminal input\n"); err != nil {
		t.Fatal(err)
	}
	if err := stdinWrite.Close(); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = stdinRead
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = stdinRead.Close()
	})

	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrWrite
	t.Cleanup(func() {
		os.Stderr = oldStderr
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
	})

	got, ok, err := Pick(filepath.Join(root, "pick"))
	if err != nil || !ok || got != picked {
		t.Fatalf("Pick() = %q, %t, %v; want %q", got, ok, err, picked)
	}
	if err := stderrWrite.Close(); err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(stderrRead)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(stdinCapture); err != nil || string(got) != "terminal input\n" {
		t.Fatalf("picker shell stdin = %q, %v; want inherited terminal input", got, err)
	}
	if string(stderr) != "picker diagnostic\n" {
		t.Fatalf("picker stderr = %q; want inherited diagnostic", stderr)
	}
}

func TestPickRejectsNonRegularSelections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the modal picker uses a POSIX shell")
	}
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fzf"), []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s\\0' \"$GAX_TEST_FZF_SELECTION\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GAX_TEST_FZF_SELECTION", "./missing.txt")
	if got, ok, err := Pick(filepath.Join(dir, "missing")); err == nil || ok || got != "" {
		t.Fatalf("Pick() = %q, %t, %v; want invalid selection error", got, ok, err)
	}
}

func TestPickRejectsSelectionExceedingTheFixedRecordCapacity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the modal picker uses a POSIX shell")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fzf"), []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s' \"$GAX_TEST_FZF_SELECTION\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GAX_TEST_FZF_SELECTION", strings.Repeat("x", selectionCapacity))
	if got, ok, err := Pick("prefix"); err == nil || ok || got != "" || !strings.Contains(err.Error(), "selection is too long") {
		t.Fatalf("Pick() = %q, %t, %v; want a fixed-capacity selection error", got, ok, err)
	}
}

func TestPickForcesUTF8ForFZFAndPreservesPinnedLocaleCategories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the modal picker uses a POSIX shell")
	}
	root := t.TempDir()
	picked := filepath.Join(root, "picked.txt")
	if err := os.WriteFile(picked, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	locale := filepath.Join(bin, "locale")
	localeScript := "#!/bin/sh\nif [ -n \"${LC_ALL-}\" ]; then\n  value=$LC_ALL\nelse\n  value=${LC_CTYPE-}\nfi\ncase \"$value\" in\n  C.UTF-8|C.utf8|UTF-8|en_US.UTF-8) printf 'UTF-8\\n' ;;\n  *) printf 'ANSI_X3.4-1968\\n' ;;\nesac\n"
	if err := os.WriteFile(locale, []byte(localeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fzf := filepath.Join(bin, "fzf")
	environmentPath := filepath.Join(root, "fzf-env")
	fzfScript := "#!/bin/sh\n/bin/cat >/dev/null\nprintf 'LC_ALL=%s\\nLC_CTYPE=%s\\nLC_COLLATE=%s\\nLC_MONETARY=%s\\nLC_NUMERIC=%s\\nLC_TIME=%s\\nLC_MESSAGES=%s\\n' \\\n  \"${LC_ALL-unset}\" \"${LC_CTYPE-unset}\" \"${LC_COLLATE-unset}\" \"${LC_MONETARY-unset}\" \\\n  \"${LC_NUMERIC-unset}\" \"${LC_TIME-unset}\" \"${LC_MESSAGES-unset}\" > \"$GAX_TEST_FZF_ENV\"\nprintf '%s\\0' \"$GAX_TEST_FZF_SELECTION\"\n"
	if err := os.WriteFile(fzf, []byte(fzfScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_COLLATE", "de_DE.UTF-8")
	t.Setenv("LC_MONETARY", "de_DE.UTF-8")
	t.Setenv("LC_NUMERIC", "de_DE.UTF-8")
	t.Setenv("LC_TIME", "de_DE.UTF-8")
	t.Setenv("LC_MESSAGES", "de_DE.UTF-8")
	t.Setenv("GAX_TEST_FZF_ENV", environmentPath)
	t.Setenv("GAX_TEST_FZF_SELECTION", "./picked.txt")

	got, ok, err := Pick(filepath.Join(root, "pick"))
	if err != nil || !ok || got != picked {
		t.Fatalf("Pick() = %q, %t, %v; want %q", got, ok, err, picked)
	}
	environment, err := os.ReadFile(environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LC_ALL=\n", "LC_CTYPE=C.UTF-8\n", "LC_COLLATE=C\n", "LC_MONETARY=C\n",
		"LC_NUMERIC=C\n", "LC_TIME=C\n", "LC_MESSAGES=C\n",
	} {
		if !strings.Contains(string(environment), want) {
			t.Errorf("fzf environment %q does not contain %q", environment, want)
		}
	}
}
