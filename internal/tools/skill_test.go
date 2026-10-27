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
	want := "- code-intelligence: Explore, navigate, and search codebases using structural search (ast-grep), tags/symbols (ctags), targeted regex, and git history without whole-file reads. (gax:skills/code-intelligence/SKILL.md)\n" +
		"- customize-gax: Change gax's own settings, providers, models, or presets — where config.json lives, which keys exist, what applies live, and what needs a restart. (gax:skills/customize-gax/SKILL.md)\n" +
		"- deploy: deploy helper (" + filepath.Join(canonical, ".agents", "skills", "deploy", "SKILL.md") + ")"
	if listing != want {
		t.Fatalf("skill listing = %q, want %q", listing, want)
	}

	deployPath := filepath.Join(canonical, ".agents", "skills", "deploy", "SKILL.md")
	loaded := registry.Run("skill", jsonArgs(map[string]any{"name": "deploy"}), 1).Text
	wantDeploy := "[skill: deploy (" + deployPath + ")]\n---\ndescription: deploy helper\n---\nrun the deploy steps"
	if loaded != wantDeploy {
		t.Fatalf("loaded project skill = %q, want %q", loaded, wantDeploy)
	}

	codeIntel := registry.Run("skill", jsonArgs(map[string]any{"name": "code-intelligence"}), 1).Text
	if !strings.HasPrefix(codeIntel, "[skill: code-intelligence (gax:skills/code-intelligence/SKILL.md)]\n---\nname: code-intelligence") || !strings.Contains(codeIntel, "# Code Intelligence & Navigation") {
		t.Fatalf("loaded code-intelligence skill = %q", codeIntel[:min(len(codeIntel), 200)])
	}

	builtin := registry.Run("skill", jsonArgs(map[string]any{"name": "customize-gax"}), 1).Text
	if !strings.HasPrefix(builtin, "[skill: customize-gax (gax:skills/customize-gax/SKILL.md)]\n---\nname: customize-gax") || !strings.Contains(builtin, "| `no_skills` |") {
		t.Fatalf("loaded built-in skill = %q", builtin[:min(len(builtin), 200)])
	}
}

func TestSkillToolGuardsActiveContextReuseAndReloadsWhenChangedOrCompacted(t *testing.T) {
	registry, project := skillTestRegistry(t, Options{})
	skillsDir := filepath.Join(project, ".agents", "skills")
	writeTestSkill(t, skillsDir, "deploy", "---\ndescription: deploy helper\n---\nrun the deploy steps\n")

	canonical := registry.bashWorkingDirectory()
	deployPath := filepath.Join(canonical, ".agents", "skills", "deploy", "SKILL.md")

	var active []conversation.Item
	registry.SetActiveItems(func() []conversation.Item {
		return active[conversation.ContextFloor(active):]
	})

	callArgs := jsonArgs(map[string]any{"name": "deploy"})
	first := registry.Run("skill", callArgs, 1).Text
	wantFull := "[skill: deploy (" + deployPath + ")]\n---\ndescription: deploy helper\n---\nrun the deploy steps"
	if first != wantFull {
		t.Fatalf("first load = %q, want %q", first, wantFull)
	}

	// Partial / catalog / error results do not trigger the guard.
	listing := registry.Run("skill", `{}`, 1).Text
	active = append(active, conversation.Item{
		Kind: conversation.KindToolResult, ToolName: conversation.String("skill"), Output: conversation.String(listing),
	})
	if got := registry.Run("skill", callArgs, 1).Text; got != wantFull {
		t.Fatalf("load after listing only = %q, want full document", got)
	}

	// Recording the full skill result in active conversation triggers the reuse guard.
	active = append(active, conversation.Item{
		Kind: conversation.KindToolResult, ToolName: conversation.String("skill"), Output: conversation.String(first),
	})
	wantAlready := "[skill: deploy (" + deployPath + ") — already loaded in active conversation; continue following the instructions above without reloading]"
	if got := registry.Run("skill", callArgs, 1).Text; got != wantAlready {
		t.Fatalf("second load in active context = %q, want %q", got, wantAlready)
	}

	// Modifying the skill content on disk invalidates the reuse match and reloads the new document.
	writeTestSkill(t, skillsDir, "deploy", "---\ndescription: deploy helper\n---\nrun the updated deploy steps\n")
	wantUpdated := "[skill: deploy (" + deployPath + ")]\n---\ndescription: deploy helper\n---\nrun the updated deploy steps"
	if got := registry.Run("skill", callArgs, 1).Text; got != wantUpdated {
		t.Fatalf("load after content change = %q, want %q", got, wantUpdated)
	}
	active = append(active, conversation.Item{
		Kind: conversation.KindToolResult, ToolName: conversation.String("skill"), Output: conversation.String(wantUpdated),
	})
	if got := registry.Run("skill", callArgs, 1).Text; got != wantAlready {
		t.Fatalf("repeat load of updated content = %q, want %q", got, wantAlready)
	}

	// Compaction advances the context floor past the full skill output, so the next load returns the full body again.
	active = append(active, conversation.Item{
		Kind: conversation.KindUser, Origin: conversation.OriginCompactSeed, Text: conversation.String("summary mentioning deploy"),
	})
	if got := registry.Run("skill", callArgs, 1).Text; got != wantUpdated {
		t.Fatalf("load after compaction = %q, want %q", got, wantUpdated)
	}
}

func TestSkillToolReportsUnknownNamesAndBadArguments(t *testing.T) {
	registry, _ := skillTestRegistry(t, Options{})

	unknown := registry.Run("skill", jsonArgs(map[string]any{"name": "missing"}), 1).Text
	if !strings.Contains(unknown, "unknown skill 'missing' (available: code-intelligence, customize-gax)") {
		t.Fatalf("unknown skill message = %q", unknown)
	}
	if got := registry.Run("skill", jsonArgs(map[string]any{"name": 7}), 1).Text; got != "'name' must be a string" {
		t.Fatalf("non-string name = %q", got)
	}
	if got := registry.Run("skill", `{bad`, 1).Text; !strings.HasPrefix(got, "invalid arguments: ") {
		t.Fatalf("malformed arguments = %q", got)
	}
	if got := registry.Run("skill", jsonArgs(map[string]any{"name": "  "}), 1).Text; !strings.HasPrefix(got, "- code-intelligence:") {
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
