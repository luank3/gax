package skill_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luank3/gax/internal/skill"
)

func TestDiscoverShadowingAndMultilineFrontmatter(t *testing.T) {
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "repo", "sub")
	if err := os.MkdirAll(filepath.Join(tmp, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	localSkill := filepath.Join(proj, ".agents", "skills", "code-intelligence")
	if err := os.MkdirAll(localSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	err := os.WriteFile(filepath.Join(localSkill, "SKILL.md"), []byte(`---
name: code-intelligence
description: >
  Custom project
  override description.
---
# Project Override
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	entries := skill.Discover(skill.Options{Home: tmp, CWD: proj})
	var found *skill.Entry
	for i := range entries {
		if entries[i].Name == "code-intelligence" {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected code-intelligence")
	}
	if found.Description != "Custom project override description." {
		t.Fatalf("unexpected description: %q", found.Description)
	}
	if found.BuiltinBody != "" {
		t.Fatal("expected local skill to shadow built-in")
	}
}
