package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPickerLabelUsesFirstPromptAndOpeningSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	contents := "" +
		`{"type":"session","provider":"openai","model":"old-model","git_branch":"feature/picker","git_subject":"Add searchable sessions"}` + "\n" +
		`{"type":"selection","provider":"anthropic","model":"new-model","model_label":"Sonnet","effort":"high","preset":"review"}` + "\n" +
		`{"kind":"user","text":"  inspect\n\n  the\t session  "}` + "\n" +
		`{"type":"selection","provider":"later-provider","model":"later-model"}` + "\n" +
		`{"kind":"user","text":"second prompt"}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	label := ReadPickerLabel(path, 512)
	if label.Prompt != "inspect the session" || label.Provider != "anthropic" || label.Model != "Sonnet" ||
		label.Effort != "high" || label.Preset != "review" || label.GitBranch != "feature/picker" ||
		label.GitSubject != "Add searchable sessions" {
		t.Fatalf("picker label = %+v", label)
	}
}

func TestReadPickerLabelSkipsTaskNotesAndMarksCompactedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	contents := "" +
		`{"type":"session","provider":"openai"}` + "\n" +
		`{"kind":"user","origin":"task_note","text":"task finished"}` + "\n" +
		`{"kind":"user","origin":"compact_seed","text":"summary"}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	label := ReadPickerLabel(path, 512)
	if label.Prompt != "(compacted)" || label.Provider != "openai" {
		t.Fatalf("compacted picker label = %+v", label)
	}
}

func TestReadPickerLabelFlattensAndTruncatesByDisplayCells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"user","text":"界abcde"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	label := ReadPickerLabel(path, 5)
	if label.Prompt != "界..." {
		t.Fatalf("truncated picker prompt = %q, want %q", label.Prompt, "界...")
	}
}
