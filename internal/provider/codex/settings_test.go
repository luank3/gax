package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsReadsOnlyTopLevelModelSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	contents := "model = \"gpt-5.6\" # primary\nmodel_reasoning_effort = 'high'\n[profiles.work]\nmodel = \"ignored\"\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	model, effort := LoadDefaults(home)
	if model != "gpt-5.6" || effort != "high" {
		t.Fatalf("defaults = (%q, %q), want (gpt-5.6, high)", model, effort)
	}
}

func TestLoadDefaultsToleratesMissingFile(t *testing.T) {
	model, effort := LoadDefaults(t.TempDir())
	if model != "" || effort != "" {
		t.Fatalf("missing defaults = (%q, %q), want empty values", model, effort)
	}
}
