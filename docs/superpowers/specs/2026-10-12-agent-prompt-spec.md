# Agent prompt spec

## Scope

- Inputs: a `config.Store` (`system_prompt`, `system_prompt_append`, `no_env`, `no_agents_md`,
  `no_skills`, `no_subagents`, `no_tasks`, `presets`), a model name, and
  `PromptOptions{Raw, Bare, CWD, Home, Shell}`.
- Outputs: `PromptResult{Text, Warnings}`; `ConfigureSession` copies `Text` into
  `Session.SystemPrompt` and clears `Session.Tools` when `raw` is set.
- Reads `AGENTS.md`, prompt files behind `@`, and `SKILL.md` frontmatter.
- Non-goal: provider request bodies and the `skill` tool's load path (`internal/tools/skill.go:41`).

## Invariants

### Raw and `(none)` yield an empty result

- **Rule:** Return `PromptResult{}` when `PromptOptions.Raw` is set or `system_prompt` is `(none)`,
  before any section is assembled.
- **Source:** `internal/agent/prompt.go:69` `BuildSystemPrompt` (`Raw` at `:70`, `(none)` at `:84`).
- **Assertion:** `TestBuildSystemPromptHonorsRawAndNone` in `internal/agent/prompt_test.go:13`.
- **Rejected:** Keeping `# Environment` when `(none)` is set; it sends no system message at all.

---

### Sections join in order with one blank line

- **Rule:** Join the base prompt, `system_prompt_append`, then the environment/project/skills
  suffix, separated by exactly one blank line.
- **Source:** `internal/agent/prompt.go:90` `BuildSystemPrompt`, `:153` `joinPromptSections`.
- **Assertion:** `TestBuildSystemPromptAppendsConfiguredTextAndEnvironment` in
  `internal/agent/prompt_test.go:24` expects the prefix `base\n\nextra\n\n# Environment\n`.
- **Rejected:** Adding two newlines per part; the environment section already ends in one, and the
  assertion at `internal/agent/prompt_test.go:165` fails on the resulting third newline.

---

### Task and subagent guidance is byte-exact

- **Rule:** Emit `taskPrompt` and `subagentPrompt` verbatim unless their flag disables them, and
  with `no_tasks` on swap the subagent text for the taskless variant.
- **Source:** `internal/agent/prompt.go:102` `buildEnvironmentSuffix` (taskless variant at `:110`).
- **Assertion:** `TestBuildSystemPromptTaskGuidanceMatchesUpstream` at
  `internal/agent/prompt_test.go:38` and
  `TestBuildSystemPromptTasklessSubagentGuidanceMatchesUpstream` at `:51`.
- **Rejected:** Disabling subagents when `no_tasks` is on; the taskless variant keeps delegation.

---

### Bare keeps the environment and task sections

- **Rule:** `PromptOptions.Bare` drops the subagent, project-context, and skills sections while
  `# Environment` and `# Background tasks` stay in the text.
- **Source:** `internal/agent/prompt.go:102` `buildEnvironmentSuffix`.
- **Assertion:** `TestBuildSystemPromptBareKeepsEnvironmentAndSkipsProjectGuidance` at
  `internal/agent/prompt_test.go:245`.
- **Rejected:** Gating `buildEnvironment` on `Bare`; the test requires `# Environment`.

---

### Project guidance is root-first

- **Rule:** Emit the config `AGENTS.md` first, then worktree-root-to-CWD `AGENTS.md` files, beneath
  `# Project Context` and `Project guidance below overrides the assistant defaults above.`
- **Source:** `internal/agent/prompt.go:215` `buildProjectContext` (heading at `:254`).
- **Assertion:** `TestBuildSystemPromptLoadsConfigFilesAndProjectInstructionsRootFirst` at
  `internal/agent/prompt_test.go:131`.
- **Rejected:** Reading only `<CWD>/AGENTS.md`, or listing the walk nearest-first.

---

### Skill listing omits paths

- **Rule:** Under `# Skills`, emit the literal line
  `Load a skill with the \`skill\` tool when a task matches its description:` and one
  `- <name>: <description>` bullet per entry, never a path.
- **Source:** `internal/agent/prompt.go:136` `buildSkills`.
- **Assertion:** `TestBuildSystemPromptListsSkillsWithoutPaths` in
  `internal/agent/prompt_test.go:182`.
- **Rejected:** Listing `entry.Path` so the model opens the document itself.

---

### Discovery is nearest-first with builtins last

- **Rule:** `skill.Discover` resolves a name from the nearest project `.agents/skills`, then
  `~/.config/gax/skills`, then `~/.agents/skills`, then embedded skills, sorted by name.
- **Source:** `internal/skill/discover.go:53` `Discover`.
- **Assertion:** `TestDiscoverPrefersTheNearestSkillAndFallsBackToBuiltins` at
  `internal/skill/discover_test.go:20` and
  `TestDiscoverKeepsTheNearestRootAndIgnoresSharedSymlinkAliases` at `:111`.
- **Rejected:** Letting embedded `customize-gax` win over the project skill of that name.

---

### `@` prompt values read a file or fall back

- **Rule:** A `@`-prefixed prompt value reads a file (`~` expanded, relative resolved under the gax
  config directory, absolute as-is) and falls back to the built-in prompt with a warning containing
  `prompt file` and `using the built-in prompt` when the read fails or the file exceeds
  `agentsFileCap`.
- **Source:** `internal/agent/prompt.go:402` `expandPromptValue` (oversize at `:424`).
- **Assertion:** `TestBuildSystemPromptValidatesPresetsAndWarnsOnce` at
  `internal/agent/prompt_test.go:89`.
- **Rejected:** Using truncated bytes; an over-cap prompt file is discarded whole.

---

### Presets are advertised only when complete

- **Rule:** Append `Presets (select with \`--preset <name>\`):` with one `- <name>: <description>`
  bullet per described preset whose provider is known, sorted by name, and warn once per store state
  via `store.MarkPresetWarning`.
- **Source:** `internal/agent/prompt.go:290` `buildPresetRoleList` (gate at `:302`).
- **Assertion:** `TestBuildSystemPromptListsOnlyDescribedPresetsWithKnownProviders` at
  `internal/agent/prompt_test.go:75` and `TestBuildSystemPromptValidatesPresetsAndWarnsOnce` at
  `:89`.
- **Rejected:** Advertising `unknown`, whose provider is absent, or repeating a warning each call.

## Gaps

- `DefaultSystemPrompt` (`internal/agent/prompt.go:29`) has no asserting test; the byte-exact tests
  set `system_prompt` to `base`, so edits to the shipped text pass unnoticed.
- The `~` branch of `expandPromptValue` (`internal/agent/prompt.go:411`) and the oversize branch at
  `:424` untested; only a failed read is covered, by
  `TestLoadPresetRejectsUnreadablePromptFile` in `internal/app/app_test.go:2473`.
- `buildSkills` tests no skill with an empty description (`internal/agent/prompt.go:148`); nearest:
  `TestSkillDescriptionDecodesAndFoldsYamlScalars` in `internal/skill/discover_test.go:153`.
- `readAgentsFile` (`internal/agent/prompt.go:262`) never has its `[truncated]` marker asserted.
- `ValidatePromptFile` (`internal/agent/prompt.go:433`) has no direct test; it is exercised by
  `TestBuildSystemPromptValidatesPresetsAndWarnsOnce` at `internal/agent/prompt_test.go:89`.

## Review

- `TestBuildSystemPromptHonorsRawAndNone` sets every section flag, so the `(none)` return at
  `internal/agent/prompt.go:84` is not isolated from them.
- `joinPromptSections` (`internal/agent/prompt.go:153`) is only observed through shipped section
  content; no test feeds other trailing-newline counts.
- `PromptOptions` carries its own `CWD`/`Home`, while the `skill` tool discovers with
  `registry.bashWorkingDirectory()` (`internal/tools/skill.go:41`), so the two can disagree.
