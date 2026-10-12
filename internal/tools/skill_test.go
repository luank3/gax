package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/conversation"
)

func writeTestSkill(t *testing.T, root, name, body string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// skillTestRegistry isolates discovery: a temporary home, no gax config directory, and a working
// directory whose only skill is the project one.
func skillTestRegistry(t *testing.T, options Options) (*Registry, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	options.WorkDir = project
	registry := New(options)
	t.Cleanup(registry.Close)
	return registry, project
}

func TestSkillToolListsAndLoadsSkills(t *testing.T) {
	registry, project := skillTestRegistry(t, Options{})
	writeTestSkill(t, filepath.Join(project, ".agents", "skills"), "deploy", "---\ndescription: deploy helper\n---\nrun the deploy steps\n")

	listing := registry.Run("skill", `{}`, 1).Text
	// The registry resolves the working directory before it lists skills, and on macOS a temporary
	// directory is reached through /var while the resolved form is /private/var.
	canonical := registry.bashWorkingDirectory()
	if canonical == "" {
		t.Fatal("registry has no working directory")
	}
	want := "- customize-gax: Change gax's own settings, providers, models, or presets — where config.json lives, which keys exist, what applies live, and what needs a restart. (gax:skills/customize-gax/SKILL.md)\n" +
		"- deploy: deploy helper (" + filepath.Join(canonical, ".agents", "skills", "deploy", "SKILL.md") + ")"
	if listing != want {
		t.Fatalf("skill listing = %q, want %q", listing, want)
	}

	loaded := registry.Run("skill", jsonArgs(map[string]any{"name": "deploy"}), 1).Text
	if loaded != "---\ndescription: deploy helper\n---\nrun the deploy steps" {
		t.Fatalf("loaded project skill = %q", loaded)
	}

	builtin := registry.Run("skill", jsonArgs(map[string]any{"name": "customize-gax"}), 1).Text
	if !strings.HasPrefix(builtin, "---\nname: customize-gax") || !strings.Contains(builtin, "| `no_skills` |") {
		t.Fatalf("loaded built-in skill = %q", builtin[:min(len(builtin), 200)])
	}
}

func TestSkillToolReportsUnknownNamesAndBadArguments(t *testing.T) {
	registry, _ := skillTestRegistry(t, Options{})

	unknown := registry.Run("skill", jsonArgs(map[string]any{"name": "missing"}), 1).Text
	if !strings.Contains(unknown, "unknown skill 'missing' (available: customize-gax)") {
		t.Fatalf("unknown skill message = %q", unknown)
	}
	if got := registry.Run("skill", jsonArgs(map[string]any{"name": 7}), 1).Text; got != "'name' must be a string" {
		t.Fatalf("non-string name = %q", got)
	}
	if got := registry.Run("skill", `{bad`, 1).Text; !strings.HasPrefix(got, "invalid arguments: ") {
		t.Fatalf("malformed arguments = %q", got)
	}
	if got := registry.Run("skill", jsonArgs(map[string]any{"name": "  "}), 1).Text; !strings.HasPrefix(got, "- customize-gax:") {
		t.Fatalf("blank name should list skills: %q", got)
	}
}

func TestSkillToolDisable(t *testing.T) {
	registry, _ := skillTestRegistry(t, Options{NoSkills: true})
	if got := registry.Run("skill", `{}`, 1).Text; got != "skills are disabled (no_skills)" {
		t.Fatalf("no_skills execution = %q", got)
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"no_skills":true}`)); err != nil {
		t.Fatal(err)
	}
	registry.store = store
	if got := registry.Run("skill", `{}`, 1).Text; got != "skills are disabled (no_skills)" {
		t.Fatalf("no_skills store execution = %q", got)
	}
}

func TestSkillToolDefinitionsAndDisable(t *testing.T) {
	definitions := New(Options{}).Definitions()
	last := definitions[len(definitions)-1]
	if last.Name != "skill" || len(last.Params) != 1 || last.Params[0].Name != "name" || last.Params[0].Required {
		t.Fatalf("skill definition = %#v", last)
	}
	disabled := New(Options{NoSkills: true}).Definitions()
	for _, definition := range disabled {
		if definition.Name == "skill" {
			t.Fatal("no_skills still advertised the skill tool")
		}
	}
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(`{"no_skills":true}`)); err != nil {
		t.Fatal(err)
	}
	fromConfig := NewFromConfig(store)
	defer fromConfig.Close()
	if len(fromConfig.Definitions()) != 7 {
		t.Fatalf("no_skills config advertised %d tools", len(fromConfig.Definitions()))
	}
}

func TestSkillToolPresentation(t *testing.T) {
	registry := New(Options{})
	call := conversation.Item{
		Kind: conversation.KindToolCall, ToolName: conversation.String("skill"),
		Arguments: conversation.String(jsonArgs(map[string]any{"name": "deploy"})),
	}
	presentation := registry.Presentation(call)
	if presentation.Argument != "deploy" || !presentation.HasArgument || presentation.RawArgument {
		t.Fatalf("skill load presentation = %+v", presentation)
	}
	call.Arguments = conversation.String("{}")
	presentation = registry.Presentation(call)
	if !presentation.HasArgument || presentation.Argument != "" || presentation.RawArgument {
		t.Fatalf("listing presentation = %+v", presentation)
	}
	call.Arguments = conversation.String(`{bad`)
	presentation = registry.Presentation(call)
	if !presentation.HasArgument || !presentation.RawArgument {
		t.Fatalf("malformed presentation = %+v", presentation)
	}
}
