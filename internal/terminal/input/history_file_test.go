package input_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/terminal/input"
)

func TestHistoryFileLoadsReadOnlyWhenPersistenceIsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "history")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\\nentry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	core := input.NewCore()
	history := input.OpenHistory(core, path, false)
	history.Add(core, "new prompt")
	if got := core.HistoryEntries(); !reflect.DeepEqual(got, []string{"old\nentry", "new prompt"}) {
		t.Fatalf("history = %#v", got)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old\\nentry\n" {
		t.Fatalf("history file changed with persistence disabled: %q, %v", got, err)
	}
}

func TestHistoryFilePersistsAndDeduplicatesPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history")
	core := input.NewCore()
	history := input.OpenHistory(core, path, true)
	history.Add(core, "one\ntwo")
	history.Add(core, "one\ntwo")
	want := "one\\ntwo\n"
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("history file = %q, %v; want %q", got, err, want)
	}

	reloaded := input.NewCore()
	input.OpenHistory(reloaded, path, false)
	if got := reloaded.HistoryEntries(); !reflect.DeepEqual(got, []string{"one\ntwo"}) {
		t.Fatalf("reloaded history = %#v", got)
	}
}

func TestHistoryFileSessionOnlyEntryPersistsWhenResubmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	core := input.NewCore()
	history := input.OpenHistory(core, path, true)
	history.AddSession(core, "cleared draft")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session-only add created history file: %v", err)
	}
	history.Add(core, "cleared draft")
	if got, err := os.ReadFile(path); err != nil || string(got) != "cleared draft\n" {
		t.Fatalf("resubmitted session entry = %q, %v", got, err)
	}
}

func TestHistoryFileSkipsOversizedRecordsAndKeepsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	body := strings.Repeat("x", 70*1024) + "\nlast\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	core := input.NewCore()
	input.OpenHistory(core, path, false)
	if got := core.HistoryEntries(); !reflect.DeepEqual(got, []string{"last"}) {
		t.Fatalf("history after oversized record = %#v", got)
	}
}
