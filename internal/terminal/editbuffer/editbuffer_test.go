package editbuffer

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEditBufferRunsConfiguredEditorAndNormalizesResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the configured editor handoff uses a POSIX shell")
	}
	dir := t.TempDir()
	editor := filepath.Join(dir, "fake-editor")
	script := "#!/bin/sh\n[ \"$1\" = --test ] || exit 4\n[ \"$2\" ] || exit 5\n[ \"$(cat \"$2\")\" = draft ] || exit 6\nprintf 'changed\\000text\\n\\n' > \"$2\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VISUAL", "fake-editor --test")
	t.Setenv("EDITOR", "missing-editor")

	got, ok, err := EditBuffer("draft", strings.NewReader(""), io.Discard, io.Discard)
	if err != nil || !ok || got != "changed text\n" {
		t.Fatalf("EditBuffer() = %q, %t, %v; want normalized edit", got, ok, err)
	}
}

func TestEditBufferForcesUTF8ForEditorWhenLCAllIsPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the configured editor handoff uses a POSIX shell")
	}
	dir := t.TempDir()
	locale := filepath.Join(dir, "locale")
	localeScript := "#!/bin/sh\nif [ -n \"${LC_ALL-}\" ]; then value=$LC_ALL; else value=${LC_CTYPE-}; fi\ncase \"$value\" in C.UTF-8|C.utf8|UTF-8|en_US.UTF-8) printf 'UTF-8\\n' ;; *) printf 'ANSI_X3.4-1968\\n' ;; esac\n"
	if err := os.WriteFile(locale, []byte(localeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(dir, "editor-env")
	editor := filepath.Join(dir, "fake-editor")
	editorScript := "#!/bin/sh\nprintf 'LC_ALL=%s\\nLC_CTYPE=%s\\nLC_COLLATE=%s\\nLC_MONETARY=%s\\nLC_NUMERIC=%s\\nLC_TIME=%s\\nLC_MESSAGES=%s\\n' \\\n  \"${LC_ALL-unset}\" \"${LC_CTYPE-unset}\" \"${LC_COLLATE-unset}\" \"${LC_MONETARY-unset}\" \\\n  \"${LC_NUMERIC-unset}\" \"${LC_TIME-unset}\" \"${LC_MESSAGES-unset}\" > \"$GAX_TEST_EDITOR_ENV\"\nprintf 'edited\\n' > \"$2\"\n"
	if err := os.WriteFile(editor, []byte(editorScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VISUAL", "fake-editor --test")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_COLLATE", "de_DE.UTF-8")
	t.Setenv("LC_MONETARY", "de_DE.UTF-8")
	t.Setenv("LC_NUMERIC", "de_DE.UTF-8")
	t.Setenv("LC_TIME", "de_DE.UTF-8")
	t.Setenv("LC_MESSAGES", "de_DE.UTF-8")
	t.Setenv("GAX_TEST_EDITOR_ENV", environmentPath)

	if got, ok, err := EditBuffer("draft", strings.NewReader(""), io.Discard, io.Discard); err != nil || !ok || got != "edited" {
		t.Fatalf("EditBuffer() = %q, %t, %v; want edited text", got, ok, err)
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
			t.Errorf("editor environment %q does not contain %q", environment, want)
		}
	}
}

func TestEditBufferDoesNotFallbackFromBrokenVisual(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the configured editor handoff uses a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "working-editor"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VISUAL", "missing-editor")
	t.Setenv("EDITOR", "working-editor")
	if text, ok, err := EditBuffer("draft", strings.NewReader(""), io.Discard, io.Discard); err == nil || ok || text != "" {
		t.Fatalf("EditBuffer() = %q, %t, %v; want configured VISUAL error", text, ok, err)
	}
}
