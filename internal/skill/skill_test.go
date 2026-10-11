package skill

import (
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
)

func TestBuiltinSkillsAreOrderedAndReadableByPath(t *testing.T) {
	skills := Builtin()
	if len(skills) == 0 {
		t.Fatal("no built-in skills are embedded")
	}
	for index, entry := range skills {
		if index > 0 && skills[index-1].Name >= entry.Name {
			t.Fatalf("built-in skills are not in name order: %q before %q", skills[index-1].Name, entry.Name)
		}
		want := "gax:skills/" + entry.Name + "/SKILL.md"
		if entry.Path != want {
			t.Errorf("path = %q, want %q", entry.Path, want)
		}
		if !strings.HasPrefix(entry.Body, "---\n") {
			t.Errorf("%s has no frontmatter: %q", entry.Name, entry.Body[:min(20, len(entry.Body))])
		}
		body, ok := Read(entry.Path)
		if !ok || body != entry.Body {
			t.Errorf("Read(%q) = %v, %v", entry.Path, len(body), ok)
		}
	}
	if _, ok := Read("gax:skills/missing/SKILL.md"); ok {
		t.Error("Read resolved a path that is not a built-in skill")
	}
	if _, ok := Read("/tmp/SKILL.md"); ok {
		t.Error("Read resolved a filesystem path")
	}
}

func TestBuiltinSkillsAreCopies(t *testing.T) {
	first := Builtin()
	first[0].Name = "mutated"
	if Builtin()[0].Name == "mutated" {
		t.Fatal("Builtin exposed the package's own slice")
	}
}

func TestBuiltinConfigSkillDocumentsEveryRegisteredSetting(t *testing.T) {
	body, ok := Read("gax:skills/customize-gax/SKILL.md")
	if !ok {
		t.Fatal("the customize-gax skill is missing")
	}
	if strings.Contains(body, settingsMarker) {
		t.Fatalf("the settings marker was not replaced: %q", settingsMarker)
	}
	settings := config.DefaultSettings()
	_, table, found := strings.Cut(body, "| Setting | Environment variable | Default | Changeable | Notes |\n")
	if !found {
		t.Fatal("the settings table header is missing")
	}
	rows := 0
	for _, line := range strings.Split(strings.TrimPrefix(table, "| --- | --- | --- | --- | --- |\n"), "\n") {
		if !strings.HasPrefix(line, "| `") {
			break
		}
		rows++
	}
	if rows != len(settings) {
		t.Errorf("settings table has %d rows, want %d", rows, len(settings))
	}
	for _, setting := range settings {
		row := "| `" + setting.Key + "` | `" + setting.EnvVar + "` |"
		if !strings.Contains(body, row) {
			t.Errorf("settings table omits %q", row)
		}
	}
	for _, want := range []string{"| `theme` | `GAX_THEME` | `auto` | live |", "| `no_skills` | `GAX_NO_SKILLS` | — | restart |"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings table omits %q", want)
		}
	}
	// A description that already enumerates its values does not repeat them.
	if strings.Contains(body, "off (auto detects from the terminal) (values:") {
		t.Error("settings table repeats the choices a description already lists")
	}
	if !strings.Contains(body, "| Skip the Environment section in the system prompt (values: on, off) |") {
		t.Error("settings table omits the values of a boolean setting")
	}
}

func TestBuiltinConfigSkillFrontmatterNamesTheSkill(t *testing.T) {
	body, ok := Read("gax:skills/customize-gax/SKILL.md")
	if !ok {
		t.Fatal("the customize-gax skill is missing")
	}
	frontmatter := body[strings.Index(body, "---\n")+len("---\n"):]
	frontmatter, _, ok = strings.Cut(frontmatter, "\n---\n")
	if !ok {
		t.Fatal("frontmatter is not terminated")
	}
	for _, line := range []string{"name: customize-gax", "description: Change gax's own settings"} {
		if !strings.Contains(frontmatter, line) {
			t.Errorf("frontmatter omits %q: %q", line, frontmatter)
		}
	}
}
