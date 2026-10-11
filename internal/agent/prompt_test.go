package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/config"
)

func TestBuildSystemPromptHonorsRawAndNone(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"configured","no_env":true,"no_agents_md":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	if result := BuildSystemPrompt(store, "model", PromptOptions{Raw: true}); result.Text != "" {
		t.Fatalf("raw prompt = %q", result.Text)
	}
	store.SetOverride("system_prompt", promptString("(none)"))
	if result := BuildSystemPrompt(store, "model", PromptOptions{}); result.Text != "" {
		t.Fatalf("none prompt = %q", result.Text)
	}
}

func TestBuildSystemPromptAppendsConfiguredTextAndEnvironment(t *testing.T) {
	root := t.TempDir()
	store := promptTestStore(t, `{"system_prompt":"base","system_prompt_append":"extra","no_agents_md":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "test-model", PromptOptions{CWD: root, Home: filepath.Dir(root), Shell: "test-shell"})
	if !strings.HasPrefix(result.Text, "base\n\nextra\n\n# Environment\n") {
		t.Fatalf("prompt section order = %q", result.Text)
	}
	for _, expected := range []string{"Working directory", "test-model", "test-shell", "Git repository: no"} {
		if !strings.Contains(result.Text, expected) {
			t.Errorf("prompt missing %q: %q", expected, result.Text)
		}
	}
}

func TestBuildSystemPromptTaskGuidanceMatchesUpstream(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_skills":true}`)
	want := "base\n\n" +
		"# Background tasks\n\n" +
		"A bash command that outlives its timeout, or is launched with `background: true`, continues as a background task. Wait on the task whose result you need next with task_wait — it returns that task's output and status. Completions of other tasks are announced automatically as one-line notes. A note ending in \"output pending\" means task_wait will return that output; a note ending in \"nothing to collect\" is final — the task is closed, and waiting on it only repeats its status. Stop a task with task_wait's `kill` flag, which also returns its final output. Never pass time with sleep or a polling loop; give task_wait a timeout instead. Tasks do not survive the gax process: in a one-shot (-p) run, tasks nobody waited on are killed once the final answer is produced. The user manages tasks with /tasks.\n\n" +
		"# Subagents\n\n" +
		"`gax -p \"<task>\"` (via the bash tool) runs a fresh gax instance with clean context in this directory and prints its final answer to stdout. Delegate to subagents only when the user asks for it. The child inherits this session's provider, model, and effort. For concurrent work, launch subagents with `background: true` and collect answers with task_wait; a synchronous call is simpler when you would otherwise wait immediately. The child prints its session id to stderr at startup; follow up on a finished (or killed) run with `gax --resume=<id> -p \"<follow-up>\"`."
	result := BuildSystemPrompt(store, "", PromptOptions{})
	if result.Text != want {
		t.Fatalf("task and subagent guidance differs from upstream:\n got: %q\nwant: %q", result.Text, want)
	}
}

func TestBuildSystemPromptTasklessSubagentGuidanceMatchesUpstream(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_skills":true,"no_tasks":true}`)
	want := "base\n\n" +
		"# Subagents\n\n" +
		"`gax -p \"<task>\"` (via the bash tool) runs a fresh gax instance with clean context in this directory and prints its final answer to stdout. Delegate to subagents only when the user asks for it. The child inherits this session's provider, model, and effort. Subagents are slow: pass a generous timeout_seconds (e.g. 1800). The child prints its session id to stderr at startup; follow up on a finished (or timed-out) run with `gax --resume=<id> -p \"<follow-up>\"`."
	result := BuildSystemPrompt(store, "", PromptOptions{})
	if result.Text != want {
		t.Fatalf("task-less subagent guidance differs from upstream:\n got: %q\nwant: %q", result.Text, want)
	}
}

func TestBuildSystemPromptUsesGaxPlatformAndResolvedShellLabels(t *testing.T) {
	root := t.TempDir()
	store := promptTestStore(t, `{"system_prompt":"base","no_agents_md":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "m", PromptOptions{
		CWD: root, Home: "/home/test", Shell: "/bin/bash",
	})
	architectureLabel := "Operating system: " + runtime.GOOS + "/" + runtime.GOARCH
	if !strings.Contains(result.Text, "- Operating system: ") || strings.Contains(strings.ToLower(result.Text), strings.ToLower(architectureLabel)) ||
		!strings.Contains(result.Text, "- Command shell: /bin/bash\n") {
		t.Fatalf("environment labels do not match gax platform details: %q", result.Text)
	}
}

func TestBuildSystemPromptListsOnlyDescribedPresetsWithKnownProviders(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"base","presets":{
		"zeta":{"provider":"openai","description":"Check for regressions"},
		"alpha":{"provider":"anthropic","description":"Inspect API boundaries"},
		"hidden":{"provider":"openai","model":"m"},
		"unknown":{"provider":"not-installed","description":"Do not advertise"}
	},"no_env":true,"no_agents_md":true,"no_skills":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{})
	want := "Presets (select with `--preset <name>`):\n- alpha: Inspect API boundaries\n- zeta: Check for regressions\n"
	if !strings.Contains(result.Text, want) || strings.Contains(result.Text, "Do not advertise") || strings.Contains(result.Text, "- hidden:") {
		t.Fatalf("preset role list mismatch: want %q in %q", want, result.Text)
	}
}

func TestBuildSystemPromptValidatesPresetsAndWarnsOnce(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"base","presets":{
		"unknown_provider":{"provider":"not-installed","description":"Do not advertise"},
		"invalid_field":{"provider":"openai","description":"Do not advertise","command":"rm"},
		"invalid_prompt":{"provider":"openai","description":"Do not advertise","system_prompt":"@/missing/preset-prompt"},
		"invalid_private":{"provider":"openai","model":[]}
	},"no_env":true,"no_agents_md":true,"no_skills":true,"no_tasks":true}`)

	first := BuildSystemPrompt(store, "", PromptOptions{})
	if strings.Contains(first.Text, "Do not advertise") || len(first.Warnings) != 4 {
		t.Fatalf("invalid presets should be omitted and diagnosed: text=%q warnings=%q", first.Text, first.Warnings)
	}
	for _, want := range []string{"unknown provider", "not presettable", "prompt file", "must be a scalar"} {
		found := false
		for _, warning := range first.Warnings {
			if strings.Contains(warning, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warnings %q do not include %q", first.Warnings, want)
		}
	}

	second := BuildSystemPrompt(store, "", PromptOptions{})
	if len(second.Warnings) != 0 {
		t.Fatalf("preset warnings repeated on prompt rebuild: %q", second.Warnings)
	}
	if err := store.LoadConfig([]byte(`{"system_prompt":"base","presets":{
		"unknown_provider":{"provider":"not-installed","description":"Do not advertise"},
		"invalid_field":{"provider":"openai","description":"Do not advertise","command":"rm"},
		"invalid_prompt":{"provider":"openai","description":"Do not advertise","system_prompt":"@/missing/preset-prompt"},
		"invalid_private":{"provider":"openai","model":[]}
	},"no_env":true,"no_agents_md":true,"no_skills":true,"no_tasks":true}`)); err != nil {
		t.Fatal(err)
	}
	if third := BuildSystemPrompt(store, "", PromptOptions{}); len(third.Warnings) != 4 {
		t.Fatalf("reloaded preset definitions should be diagnosed again: %q", third.Warnings)
	}
}

func TestBuildSystemPromptLoadsConfigFilesAndProjectInstructionsRootFirst(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	globalPath := filepath.Join(configHome, "gax", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte("global rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(project, "AGENTS.md"): "root rules\n",
		filepath.Join(nested, "AGENTS.md"):  "nested rules\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: nested, Home: project})
	global, root, child := strings.Index(result.Text, "global rules"), strings.Index(result.Text, "root rules"), strings.Index(result.Text, "nested rules")
	if global < 0 || root < global || child < root {
		t.Fatalf("project instructions were missing or not root-first: %q", result.Text)
	}
}

func TestBuildSystemPromptUsesUpstreamSpacingBeforeProjectContext(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_tasks":true,"no_subagents":true,"no_skills":true}`)
	result := BuildSystemPrompt(store, "model", PromptOptions{CWD: project, Home: filepath.Dir(project), Shell: "/bin/sh"})
	heading := strings.Index(result.Text, "# Project Context")
	if heading < 0 {
		t.Fatalf("project context was missing: %q", result.Text)
	}
	before := result.Text[:heading]
	if trailing := len(before) - len(strings.TrimRight(before, "\n")); trailing != 2 {
		t.Fatalf("environment/project spacing differs from upstream: %q", result.Text)
	}
}

func TestBuildSystemPromptPreservesSkillListTrailingNewline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".agents", "skills", "guide")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: guide\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: t.TempDir(), Home: home})
	if !strings.HasSuffix(result.Text, "- guide (~/.agents/skills/guide/SKILL.md)\n") {
		t.Fatalf("skill list terminator differs from upstream: %q", result.Text)
	}
}

func TestBuildSystemPromptListsBuiltinSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: t.TempDir(), Home: home})
	if !strings.Contains(result.Text, "- customize-gax: Change gax's own settings, providers, models, or presets") {
		t.Fatalf("built-in skill is not listed: %q", result.Text)
	}
	if !strings.Contains(result.Text, "(gax:skills/customize-gax/SKILL.md)") {
		t.Fatalf("built-in skill path is not listed: %q", result.Text)
	}
	disabled := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	if result := BuildSystemPrompt(disabled, "", PromptOptions{CWD: t.TempDir(), Home: home}); strings.Contains(result.Text, "customize-gax") {
		t.Fatalf("no_skills kept the built-in skill: %q", result.Text)
	}
}

func TestBuildSystemPromptDiscoveredSkillShadowsBuiltinSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(project, ".agents", "skills", "customize-gax")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\ndescription: project override\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: project, Home: home})
	if !strings.Contains(result.Text, "- customize-gax: project override (") {
		t.Fatalf("discovered skill did not shadow the built-in: %q", result.Text)
	}
	if strings.Contains(result.Text, "gax:skills/") {
		t.Fatalf("the built-in skill was listed beside its shadow: %q", result.Text)
	}
}

func TestBuildSystemPromptPreservesProjectInstructionTrailingNewline(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":false,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: project, Home: filepath.Dir(project)})
	if !strings.HasSuffix(result.Text, "project rule\n") {
		t.Fatalf("project instruction terminator differs from upstream: %q", result.Text)
	}
}

func TestBuildSystemPromptFallsBackWhenConfiguredPromptFileIsMissing(t *testing.T) {
	store := promptTestStore(t, `{"system_prompt":"@/missing/prompt","no_env":true,"no_agents_md":true,"no_skills":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{})
	if !strings.HasPrefix(result.Text, DefaultSystemPrompt) || len(result.Warnings) != 1 {
		t.Fatalf("fallback result = %#v", result)
	}
}

func TestBuildSystemPromptExpandsPromptFilesAndListsNearestSkillsFirst(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", filepath.Join(configHome, "home"))
	promptFile := filepath.Join(configHome, "gax", "prompt.txt")
	if err := os.MkdirAll(filepath.Dir(promptFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptFile, []byte("prompt from file\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill := func(root, name, description string) {
		t.Helper()
		path := filepath.Join(root, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\ndescription: "+description+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(filepath.Join(project, ".agents", "skills"), "shared", "ancestor description")
	writeSkill(filepath.Join(nested, ".agents", "skills"), "shared", "nearest description")
	writeSkill(filepath.Join(nested, ".agents", "skills"), "local", "local description")
	store := promptTestStore(t, `{"system_prompt":"@prompt.txt","no_env":true,"no_agents_md":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: nested, Home: filepath.Join(configHome, "home")})
	if !strings.HasPrefix(result.Text, "prompt from file") || len(result.Warnings) != 0 {
		t.Fatalf("prompt file expansion = %#v", result)
	}
	if !strings.Contains(result.Text, "- shared: nearest description") || strings.Contains(result.Text, "ancestor description") {
		t.Fatalf("nearest skill did not shadow ancestor: %q", result.Text)
	}
	if strings.Index(result.Text, "- local:") > strings.Index(result.Text, "- shared:") {
		t.Fatalf("skills were not sorted by name: %q", result.Text)
	}
}

func TestBuildSystemPromptBareKeepsEnvironmentAndSkipsProjectGuidance(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_skills":false,"no_subagents":false,"no_tasks":false}`)
	result := BuildSystemPrompt(store, "m", PromptOptions{CWD: project, Bare: true, Shell: "sh"})
	if !strings.Contains(result.Text, "# Environment") || !strings.Contains(result.Text, "# Background tasks") {
		t.Fatalf("bare mode dropped base context: %q", result.Text)
	}
	if strings.Contains(result.Text, "project rules") || strings.Contains(result.Text, "# Subagents") || strings.Contains(result.Text, "# Skills") {
		t.Fatalf("bare mode kept project/delegation guidance: %q", result.Text)
	}
}

func TestBuildSystemPromptSharedSkillSymlinkDoesNotShadowGaxSkills(t *testing.T) {
	home := t.TempDir()
	sharedRoot := filepath.Join(home, ".agents", "skills")
	writeSkill := func(root, name, description string) {
		t.Helper()
		directory := filepath.Join(root, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\ndescription: " + description + "\n---\n"
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(sharedRoot, "duplicate", "shared skill")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeSkill(filepath.Join(configHome, "gax", "skills"), "duplicate", "gax skill")
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectSkills := filepath.Join(project, ".agents", "skills")
	if err := os.MkdirAll(filepath.Dir(projectSkills), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedRoot, projectSkills); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := promptTestStore(t, `{"system_prompt":"base","no_env":true,"no_agents_md":true,"no_subagents":true,"no_tasks":true}`)
	result := BuildSystemPrompt(store, "", PromptOptions{CWD: project, Home: home})
	if !strings.Contains(result.Text, "- duplicate: gax skill (") || strings.Contains(result.Text, "shared skill") {
		t.Fatalf("shared skill symlink shadowed the higher-priority gax skill: %q", result.Text)
	}
}

func TestSkillDescriptionDecodesAndFoldsYamlScalars(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "plain comment", data: "---\ndescription: Build for AWS: ECS # deployment\n---\n", want: "Build for AWS: ECS"},
		{name: "folded block", data: "---\ndescription: >\n  Build reliable services.\n  Verify deployment safety.\n---\n", want: "Build reliable services. Verify deployment safety."},
		{name: "quoted continuation", data: "---\ndescription: 'it''s a\n  multi-line # description'\n---\n", want: "it's a multi-line # description"},
		{name: "escaped unicode", data: "---\ndescription: \"Use caf\\u00e9 and \\U0001F600\"\n---\n", want: "Use café and 😀"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := skillDescription([]byte(test.data)); got != test.want {
				t.Fatalf("skill description = %q, want %q", got, test.want)
			}
		})
	}
}

func promptTestStore(t *testing.T, data string) *config.Store {
	t.Helper()
	store := config.New(config.DefaultSettings())
	store.SetEnvironment(nil)
	if err := store.LoadConfig([]byte(data)); err != nil {
		t.Fatal(err)
	}
	return store
}

func promptString(value string) *string { return &value }
