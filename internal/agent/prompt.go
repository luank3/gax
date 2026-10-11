package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/luank3/gax/internal/config"
	"github.com/luank3/gax/internal/provider/registry"
	systemfs "github.com/luank3/gax/internal/system/fs"
	terminalgit "github.com/luank3/gax/internal/system/git"
	terminalosinfo "github.com/luank3/gax/internal/system/osinfo"
	systempath "github.com/luank3/gax/internal/system/path"
	systemshell "github.com/luank3/gax/internal/system/shell"
)

const (
	agentsFileCap       = 64 * 1024
	skillFrontmatterCap = 8 * 1024
	skillDescriptionCap = 1024
	projectWalkMaxDepth = 64
)

const DefaultSystemPrompt = "You are gax, a minimalist coding assistant running in the user's terminal.\n" +
	"\n" +
	"Prefer action over explanation: when a question can be answered by running a command or reading a file, do so. Be concise: no filler, no trailing summaries. Reference code as path:line. Before substantial work, say in one sentence what you're about to do; while working, mention only meaningful developments (a root cause, a change of direction, a blocker worth a decision), not routine steps.\n" +
	"\n" +
	"When something is ambiguous, infer from the code and pick a sensible default rather than stopping. Ask only when genuinely blocked: the choice materially changes the result, an action is destructive or affects shared state, or you need a value you can't obtain. To ask, end your turn with one targeted question and a recommended default.\n" +
	"\n" +
	"Prefer read/edit/write for ordinary file operations. Bash remains useful for searches, pipelines, formatters, and bulk transformations.\n" +
	"\n" +
	"When changing code:\n" +
	"- Make the smallest correct change that fits the existing style.\n" +
	"- Fix root causes, not symptoms. Don't fix unrelated bugs unless asked.\n" +
	"- Don't introduce new abstractions, helpers, or compatibility shims unless the task genuinely needs them.\n" +
	"- Add a comment only when the *why* is non-obvious.\n" +
	"- If the project has a build, tests, or linter, run them before reporting done.\n" +
	"\n" +
	"Git: never commit, push, amend, branch, or run destructive commands (`reset --hard`, `checkout --`, `branch -D`) unless the user explicitly asks. Never revert changes you didn't make. If a hook or check fails, fix the cause; don't bypass with `--no-verify`.\n" +
	"\n" +
	"If asked for a \"review\": lead with bugs, risks, and missing tests for the *proposed change*, not a summary. A finding should be one the author would fix if they knew. Skip pre-existing issues and trivial style. Calibrate severity honestly; no flattery. Empty findings is a valid result."

const taskPrompt = "# Background tasks\n\n" +
	"A bash command that outlives its timeout, or is launched with `background: true`, continues as a background task. Wait on the task whose result you need next with task_wait — it returns that task's output and status. Completions of other tasks are announced automatically as one-line notes. A note ending in \"output pending\" means task_wait will return that output; a note ending in \"nothing to collect\" is final — the task is closed, and waiting on it only repeats its status. Stop a task with task_wait's `kill` flag, which also returns its final output. Never pass time with sleep or a polling loop; give task_wait a timeout instead. Tasks do not survive the gax process: in a one-shot (-p) run, tasks nobody waited on are killed once the final answer is produced. The user manages tasks with /tasks."

const subagentPrompt = "# Subagents\n\n" +
	"`gax -p \"<task>\"` (via the bash tool) runs a fresh gax instance with clean context in this directory and prints its final answer to stdout. Delegate to subagents only when the user asks for it. The child inherits this session's provider, model, and effort. For concurrent work, launch subagents with `background: true` and collect answers with task_wait; a synchronous call is simpler when you would otherwise wait immediately. The child prints its session id to stderr at startup; follow up on a finished (or killed) run with `gax --resume=<id> -p \"<follow-up>\"`."

type PromptOptions struct {
	Raw   bool
	Bare  bool
	CWD   string
	Home  string
	Shell string
}

type PromptResult struct {
	Text     string
	Warnings []string
}

// BuildSystemPrompt assembles the configured base prompt and its environment/project suffix.
// The caller owns display of warnings returned when an optional prompt file cannot be read.
func BuildSystemPrompt(store *config.Store, model string, options PromptOptions) PromptResult {
	if options.Raw {
		return PromptResult{}
	}
	if options.CWD == "" {
		if cwd, err := os.Getwd(); err == nil {
			options.CWD = cwd
		} else {
			options.CWD = "(unknown)"
		}
	}
	if options.Home == "" {
		options.Home = os.Getenv("HOME")
	}
	baseValue := configValue(store, "system_prompt")
	if baseValue != nil && *baseValue == "(none)" {
		return PromptResult{}
	}
	if options.Shell == "" && !configBool(store, "no_env") {
		options.Shell = configuredShell(store)
	}
	base, warnings := expandPromptValue(baseValue, DefaultSystemPrompt)
	appendText, appendWarnings := expandPromptValue(configValue(store, "system_prompt_append"), "")
	warnings = append(warnings, appendWarnings...)
	var parts []string
	appendPromptPart(&parts, base)
	appendPromptPart(&parts, appendText)
	suffix, presetWarnings := buildEnvironmentSuffix(store, model, options)
	appendPromptPart(&parts, suffix)
	warnings = append(warnings, presetWarnings...)
	return PromptResult{Text: strings.Join(parts, "\n\n"), Warnings: warnings}
}

func buildEnvironmentSuffix(store *config.Store, model string, options PromptOptions) (string, []string) {
	var parts []string
	var warnings []string
	if !configBool(store, "no_tasks") {
		parts = append(parts, taskPrompt)
	}
	if !options.Bare && !configBool(store, "no_subagents") {
		subagents := subagentPrompt
		if configBool(store, "no_tasks") {
			subagents = "# Subagents\n\n`gax -p \"<task>\"` (via the bash tool) runs a fresh gax instance with clean context in this directory and prints its final answer to stdout. Delegate to subagents only when the user asks for it. The child inherits this session's provider, model, and effort. Subagents are slow: pass a generous timeout_seconds (e.g. 1800). The child prints its session id to stderr at startup; follow up on a finished (or timed-out) run with `gax --resume=<id> -p \"<follow-up>\"`."
		}
		presetRoles, presetWarnings := buildPresetRoleList(store)
		subagents += presetRoles
		warnings = append(warnings, presetWarnings...)
		parts = append(parts, subagents)
	}
	if !configBool(store, "no_env") {
		parts = append(parts, buildEnvironment(model, options))
	}
	if !options.Bare && !configBool(store, "no_agents_md") {
		if project := buildProjectContext(store, options); project != "" {
			parts = append(parts, project)
		}
	}
	if !options.Bare && !configBool(store, "no_skills") {
		if skills := buildSkills(options); skills != "" {
			parts = append(parts, skills)
		}
	}
	return joinPromptSections(parts), warnings
}

func joinPromptSections(parts []string) string {
	var out strings.Builder
	trailingNewlines := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out.Len() > 0 && trailingNewlines < 2 {
			out.WriteString(strings.Repeat("\n", 2-trailingNewlines))
		}
		out.WriteString(part)
		trailingNewlines = len(part) - len(strings.TrimRight(part, "\n"))
	}
	return out.String()
}

func buildEnvironment(model string, options PromptOptions) string {
	if options.CWD == "" {
		return ""
	}
	cwd := cleanPromptText(collapseHome(options.CWD, options.Home))
	var out strings.Builder
	out.WriteString("# Environment\n\n")
	fmt.Fprintf(&out, "- Working directory: %s\n", cwd)
	if options.Home != "" {
		fmt.Fprintf(&out, "- Home directory: %s\n", cleanPromptText(options.Home))
	}
	fmt.Fprintf(&out, "- Operating system: %s\n", terminalosinfo.Description())
	fmt.Fprintf(&out, "- Command shell: %s\n", cleanPromptText(options.Shell))
	if model != "" {
		fmt.Fprintf(&out, "- Model: %s\n", cleanPromptText(model))
	}
	if root := terminalgit.FindWorktreeRoot(options.CWD); root != "" {
		fmt.Fprintf(&out, "- Git repository root: %s\n", cleanPromptText(collapseHome(root, options.Home)))
	} else {
		out.WriteString("- Git repository: no\n")
	}

	probes := []struct{ name, replacement string }{
		{"rg", "grep -r"}, {"fd", "find"}, {"jq", ""}, {"gh", ""},
		{"python3", "python"}, {"node", ""}, {"magick", ""},
	}
	var available []string
	var preferred []string
	for _, probe := range probes {
		if _, err := exec.LookPath(probe.name); err != nil {
			continue
		}
		available = append(available, "`"+probe.name+"`")
		if probe.replacement != "" {
			preferred = append(preferred, "`"+probe.name+"` to `"+probe.replacement+"`")
		}
	}
	if len(available) > 0 {
		fmt.Fprintf(&out, "\nAvailable command-line tools: %s.\n", strings.Join(available, ", "))
	}
	if len(preferred) > 0 {
		fmt.Fprintf(&out, "Prefer %s.\n", strings.Join(preferred, ", "))
	}
	return out.String()
}

func buildProjectContext(store *config.Store, options PromptOptions) string {
	var sections []string
	if path, ok := promptConfigPath("AGENTS.md"); ok {
		if section := readAgentsFile(path, collapseHome(path, options.Home)); section != "" {
			sections = append(sections, section)
		}
	}
	if options.CWD == "" {
		return strings.Join(sections, "\n")
	}
	root := terminalgit.FindWorktreeRoot(options.CWD)
	var paths []string
	if root == "" {
		paths = append(paths, systempath.Join(options.CWD, "AGENTS.md"))
	} else {
		for dir, depth := options.CWD, 0; depth < projectWalkMaxDepth; depth++ {
			paths = append(paths, systempath.Join(dir, "AGENTS.md"))
			if filepath.Clean(dir) == filepath.Clean(root) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		for left, right := 0, len(paths)-1; left < right; left, right = left+1, right-1 {
			paths[left], paths[right] = paths[right], paths[left]
		}
	}
	for _, path := range paths {
		if section := readAgentsFile(path, collapseHome(path, options.Home)); section != "" {
			sections = append(sections, section)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("# Project Context\n\nProject guidance below overrides the assistant defaults above.\n")
	for _, section := range sections {
		out.WriteByte('\n')
		out.WriteString(section)
	}
	return out.String()
}

func readAgentsFile(path, displayPath string) string {
	data, truncated, err := systemfs.ReadFileCapped(path, agentsFileCap)
	if err != nil {
		return ""
	}
	content := cleanPromptText(string(data))
	var out strings.Builder
	fmt.Fprintf(&out, "## %s\n\n", cleanPromptText(displayPath))
	out.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		out.WriteByte('\n')
	}
	if truncated {
		out.WriteString("[truncated]\n")
	}
	return out.String()
}

type promptSkill struct{ name, path, description string }

func buildSkills(options PromptOptions) string {
	skills := make(map[string]promptSkill)
	sharedRoot := ""
	var sharedRootInfo os.FileInfo
	if options.Home != "" {
		sharedRoot = systempath.Join(options.Home, ".agents/skills")
		sharedRootInfo, _ = os.Stat(sharedRoot)
	}
	if options.CWD != "" {
		root := terminalgit.FindWorktreeRoot(options.CWD)
		for dir, depth := options.CWD, 0; depth < projectWalkMaxDepth; depth++ {
			projectSkills := systempath.Join(dir, ".agents/skills")
			sharedAlias := false
			if sharedRootInfo != nil {
				if projectInfo, err := os.Stat(projectSkills); err == nil && os.SameFile(projectInfo, sharedRootInfo) {
					sharedAlias = true
				}
			}
			if !sharedAlias {
				collectSkills(projectSkills, options.Home, skills)
			}
			if root == "" || filepath.Clean(root) == filepath.Clean(dir) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if directory, ok := promptConfigPath("skills"); ok {
		collectSkills(directory, options.Home, skills)
	}
	if sharedRoot != "" {
		collectSkills(sharedRoot, options.Home, skills)
	}
	if len(skills) == 0 {
		return ""
	}
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("# Skills\n\nRead the corresponding SKILL.md when a task matches the description:\n\n")
	for _, name := range names {
		skill := skills[name]
		if skill.description != "" {
			fmt.Fprintf(&out, "- %s: %s (%s)\n", skill.name, skill.description, skill.path)
		} else {
			fmt.Fprintf(&out, "- %s (%s)\n", skill.name, skill.path)
		}
	}
	return out.String()
}

func collectSkills(root, home string, skills map[string]promptSkill) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := cleanPromptText(entry.Name())
		if _, exists := skills[name]; exists {
			continue
		}
		path := systempath.Join(systempath.Join(root, entry.Name()), "SKILL.md")
		data, _, err := systemfs.ReadFileCapped(path, skillFrontmatterCap)
		if err != nil {
			continue
		}
		skills[name] = promptSkill{name: name, path: cleanPromptText(collapseHome(path, home)), description: skillDescription(data)}
	}
}

func skillDescription(data []byte) string {
	return truncatePromptUTF8(frontmatterScalarLine(data, "description"), skillDescriptionCap)
}

func configuredShell(store *config.Store) string {
	configured := configValue(store, "bash.shell")
	if configured == nil {
		path, _ := systemshell.Resolve("")
		return path
	}
	path, _ := systemshell.Resolve(*configured)
	return path
}

func buildPresetRoleList(store *config.Store) (string, []string) {
	if store == nil {
		return "", nil
	}
	providers := registry.New(store)
	names := store.ObjectKeys("presets")
	sort.Strings(names)
	var lines []string
	var warnings []string
	for _, name := range names {
		members, err := validatePromptPreset(store, name)
		if err != nil {
			if store.MarkPresetWarning(name) {
				warnings = append(warnings, fmt.Sprintf("%s — ignoring it", err))
			}
			continue
		}
		description := members["description"]
		if description == "" {
			continue
		}
		provider := members["provider"]
		if _, ok := providers.Find(provider); !ok {
			if store.MarkPresetWarning(name) {
				warnings = append(warnings, fmt.Sprintf("preset '%s' names unknown provider '%s' — not advertised to the model",
					cleanPromptText(name), cleanPromptText(provider)))
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s\n", cleanPromptText(name), cleanPromptText(description)))
	}
	if len(lines) == 0 {
		return "", warnings
	}
	return "\n\nPresets (select with `--preset <name>`):\n" + strings.Join(lines, ""), warnings
}

func validatePromptPreset(store *config.Store, name string) (map[string]string, error) {
	var members map[string]json.RawMessage
	if json.Unmarshal(presetJSON(store, name), &members) != nil || members == nil {
		return nil, fmt.Errorf("preset '%s' is not an object (define a presets.%s block)",
			cleanPromptText(name), cleanPromptText(name))
	}
	allowed := map[string]bool{
		"provider": true, "model": true, "effort": true, "system_prompt": true,
		"system_prompt_append": true, "description": true, "tint": true,
	}
	values := make(map[string]string, len(members))
	for key, raw := range members {
		if !allowed[key] {
			return nil, fmt.Errorf("preset '%s': '%s' is not presettable (allowed: provider, model, effort, system_prompt, system_prompt_append, tint); endpoint settings belong in a providers.<name> block, context/recording in the --bare/--no-session flags",
				cleanPromptText(name), cleanPromptText(key))
		}
		value, ok := promptPresetScalar(raw)
		if !ok {
			return nil, fmt.Errorf("preset '%s': '%s' must be a scalar", cleanPromptText(name), cleanPromptText(key))
		}
		values[key] = value
	}
	if values["provider"] == "" {
		return nil, fmt.Errorf("preset '%s' must name a provider", cleanPromptText(name))
	}
	for _, setting := range config.DefaultSettings() {
		if setting.Key == "tint" {
			if tint, exists := values["tint"]; exists && !setting.Valid(tint) {
				return nil, fmt.Errorf("preset '%s': unknown tint '%s' (expected %s)",
					cleanPromptText(name), cleanPromptText(tint), setting.Choices)
			}
			break
		}
	}
	for _, key := range []string{"system_prompt", "system_prompt_append"} {
		if value, exists := values[key]; exists {
			if err := ValidatePromptFile(value); err != nil {
				return nil, fmt.Errorf("preset '%s': %w", cleanPromptText(name), err)
			}
		}
	}
	return values, nil
}

func promptPresetScalar(raw json.RawMessage) (string, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case bool:
		return fmt.Sprint(typed), true
	default:
		return "", false
	}
}

func presetJSON(store *config.Store, name string) json.RawMessage {
	return store.PresetJSON(name)
}

func promptConfigPath(relative string) (string, bool) {
	return systempath.XDGGaxPath(os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME"), ".config", relative)
}

func collapseHome(path, home string) string {
	return systempath.CollapseHome(path, home)
}

func expandPromptValue(value *string, fallback string) (string, []string) {
	if value == nil {
		return fallback, nil
	}
	if !strings.HasPrefix(*value, "@") {
		return cleanPromptText(*value), nil
	}
	spec := (*value)[1:]
	path := spec
	if strings.HasPrefix(spec, "~") {
		path = systempath.ExpandHome(spec, os.Getenv("HOME"))
	} else if !strings.HasPrefix(spec, "/") {
		if resolved, ok := promptConfigPath(spec); ok {
			path = resolved
		} else {
			return fallback, []string{fmt.Sprintf("couldn't resolve prompt file %q — using the built-in prompt", spec)}
		}
	}
	data, truncated, err := systemfs.ReadFileCapped(path, agentsFileCap)
	if err != nil {
		return fallback, []string{fmt.Sprintf("couldn't read prompt file %s — using the built-in prompt", path)}
	}
	if truncated {
		return fallback, []string{fmt.Sprintf("prompt file %s exceeds %d bytes — using the built-in prompt", path, agentsFileCap)}
	}
	text := strings.TrimRight(cleanPromptText(string(data)), "\r\n")
	return text, nil
}

// ValidatePromptFile checks that a configured @file prompt can be expanded without a warning.
// Literal prompt values do not require validation.
func ValidatePromptFile(value string) error {
	if !strings.HasPrefix(value, "@") {
		return nil
	}
	_, warnings := expandPromptValue(&value, "")
	if len(warnings) > 0 {
		return errors.New(strings.TrimSuffix(warnings[0], " — using the built-in prompt"))
	}
	return nil
}

func appendPromptPart(parts *[]string, value string) {
	if value != "" {
		*parts = append(*parts, value)
	}
}

func cleanPromptText(value string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "\uFFFD"), "\uFFFD")
}

func truncatePromptUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func configValue(store *config.Store, key string) *string {
	if store == nil {
		return nil
	}
	return store.String(key)
}

func configBool(store *config.Store, key string) bool {
	return store != nil && store.Bool(key)
}

func ConfigureSession(state *Session, store *config.Store, model string, raw bool, options PromptOptions) PromptResult {
	if state == nil {
		return PromptResult{}
	}
	options.Raw = raw
	result := BuildSystemPrompt(store, model, options)
	state.SystemPrompt = result.Text
	state.RawMode = raw
	if raw {
		state.Tools = nil
	}
	return result
}
