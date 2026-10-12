# App config and selection command spec

## Scope

The `internal/app` handlers behind the slash commands that change the live provider, model, effort,
preset, or settings, dispatched from `internal/app/app.go:855-940`.
Inputs: a `slash.Parsed` command, the `config.Store`, the live provider and model, and a TTY or
plain stdin/stdout/stderr. Outputs: user-visible lines and mutations of the store,
`session.Log`, `config.json`, and `state.json`.
Non-goal: the provider-side request shapes behind `/usage` and `/model`, and the agent turn loop.

## Invariants

### A `/config` write is a run-tier override

- **Rule:** `/config <key> <value>` stores the value with `store.SetOverride` and echoes
  `<key> = <value> (run)`; an invalid value leaves the store untouched.
- **Source:** `internal/app/config_commands.go:439` (`setConfigValue`).
- **Assertion:** `TestRunREPLConfigCommandReadsValidatesAndUpdatesEditableSettings` in
  `internal/app/app_test.go:2266`.
- **Rejected:** Storing the raw argument: the test drives `/config theme LIGHT` and asserts
  `theme = light (run)`, so `canonicalConfigValue` (`internal/app/config_commands.go:600`) folds it.

---

### A setting not marked `Editable` refuses writes

- **Rule:** Writing one prints `'<key>' can't be changed from /config — use /<command>` for
  `provider`, `model`, `effort`, `preset`, and the env-var-plus-restart variant otherwise.
- **Source:** `internal/app/config_commands.go:440`, mapping `internal/app/config_commands.go:535`.
- **Assertion:** `TestRunREPLConfigCommandReadsValidatesAndUpdatesEditableSettings` in
  `internal/app/app_test.go:2266`.
- **Rejected:** Answering `provider` with the runtime message: the test pins
  `can't be changed from /config` and the `change it with /provider` hint.

---

### A preset definition has a closed member list

- **Rule:** `loadPreset` rejects any member outside `description`, `provider`, `model`, `effort`,
  `system_prompt`, `system_prompt_append`, `tint` with `is not presettable`.
- **Source:** `internal/app/config_commands.go:226` (`loadPreset`).
- **Assertion:** `TestLoadPresetReportsInvalidMembersInConfigOrder` in
  `internal/app/app_test.go:2485`.
- **Rejected:** Ignoring unknown members as metadata: the test pins the message, and
  `validPresetNamesWithWarnings` drops the whole preset because of it.

---

### Applying a preset replaces the whole selection

- **Rule:** `applyPreset` clears the active preset and prompt overrides, then writes `provider`,
  `model`, `effort`, `system_prompt`, `system_prompt_append`, sentinel-defaulting an omitted
  `model` or `effort`.
- **Source:** `internal/app/config_commands.go:305`, sentinel at `:307`.
- **Assertion:** `TestRunREPLPresetUsesProviderDefaultAndDisplaysPresetErrors` in
  `internal/app/app_test.go:2383`.
- **Rejected:** Keeping the current model when the preset has none; that test starts on
  `--model=test-model` and asserts the provider default wins.

---

### A preset effort the model does not take is dropped

- **Rule:** After a preset applies, `state.Effort` is recomputed as
  `selected.ResolveEffort(model, store.String("effort"))`, so an unsupported level resolves away.
- **Source:** `internal/app/config_commands.go:160` (`runPresetCommand`).
- **Assertion:** `TestRunREPLPresetDropsUnsupportedEffort` in `internal/app/app_test.go:2362`.
- **Rejected:** Copying the preset's `effort` into `state.Effort`; the mock provider exposes no
  levels and the test fails on the printed summary.

---

### `/preset-save` captures, then activates

- **Rule:** `/preset-save <name> [tint]` writes `presets.<name>` from the live selection, prints
  `saved preset '<name>' in config.json` or `updated preset '<name>'`, then re-runs `/preset <name>`
  so the preset is live and its name reaches `state.json`.
- **Source:** `internal/app/preset_save.go:74`, re-apply at `internal/app/preset_save.go:97`.
- **Assertion:** `TestRunREPLPresetSavePersistsAndActivatesLiveSelection` in
  `internal/app/app_test.go:2500`.
- **Rejected:** Stopping after the write; the test also asserts the `[review]` banner and
  `state["preset"] == "review"`.

---

### A discovered model is not captured into a preset

- **Rule:** `runPresetSaveCommand` stores `model` only when `provider.ModelDiscovered` is false, so
  an auto-discovered model stays live but is left out of `presets.<name>`.
- **Source:** `internal/app/preset_save.go:71`.
- **Assertion:** `TestPresetSaveOmitsDiscoveredLlamaModel` in `internal/app/app_test.go:814`.
- **Rejected:** Persisting the listed id: llama.cpp can serve another model next start, so the plan
  keeps discovered choices run-scoped
  (`docs/superpowers/plans/2026-10-10-repl-provider-selection.md:22`).

---

### Logins are managed for Codex only

- **Rule:** `/login` and `/logout` accept only `codex` (after `registry.CanonicalID`); other names
  print `no login flow for '<name>' — gax manages logins for: codex`, and both require a provider
  name when not on a live TTY.
- **Source:** `internal/app/auth_commands.go:34`, logout at `internal/app/auth_commands.go:117`.
- **Assertion:** `TestRunLoginAndLogoutCommandArgumentHandling` in `internal/app/app_test.go:1085`.
- **Rejected:** Testing `liveTTY` before `codex.HasManagedAuth`: every case runs without a TTY, so
  that order would print the name prompt for the `logout no managed credentials` case.

## Gaps

- `no presets defined in config.json — use /preset-save to save the current selection`
  (`internal/app/config_commands.go:36`) has no asserting test; the nearest indirect one is
  `TestValidPresetNamesPreserveConfigOrderAndSkipInvalidEntries` (`internal/app/app_test.go:146`).
- The `must name a provider` branches (`internal/app/config_commands.go:220`, `:249`) have no
  asserting test; `TestLoadPresetCoercesBooleanAndNumberScalarsLikeUpstream` only covers the
  coercion path through the same function.
- The declined-overwrite branch, `keep it` then `left preset '<name>' unchanged`
  (`internal/app/preset_save.go:52-56`), never runs;
  `TestRunTTYPresetSaveConfirmsOverwriteAndCanClearTint` selects `overwrite` only.
- Resolved 2026-10-12: the note at `docs/superpowers/plans/2026-10-10-repl-provider-selection.md:3`
  claiming upstream takes no arguments for `/provider`, `/model`, or `/effort` was wrong. Upstream
  master `2834c2c` takes them (`usage = "[id]"` + `argument_choices` in `src/slash.c`), which is what
  the handlers and `TestRunREPLSelectsSelectionByArgument` (`internal/app/app_test.go:1162`) do.
- Resolved 2026-10-12: `docs/superpowers/plans/2026-10-10-repl-model-selection.md:18` asks `/model`
  with no argument to print candidates in a non-TTY REPL, but upstream's `picker_run`
  (`src/terminal/picker.c:666`) returns -1 without a TTY and prints nothing, so returning silently
  when the listing holds more than one model (`internal/app/provider_commands.go:388`) is parity. The
  plan line carries a superseding note.

## Review

- `/usage` is gated on `registry.HookQueryUsage` and switched on `provider.Definition.ID`
  (`internal/app/provider_commands.go:620`); `TestRunProviderUsageReportsUnsupportedProviders` pins
  the openai note; the `default:` arm `internal/app/provider_commands.go:683` is unreached.
- The `opencode-go` usage case asks `provider.Headers(runtime.WireChat, "", false)` rather than
  `MetadataHeaders` (`internal/app/provider_commands.go:669`), per the comment on the model dialect;
  only `TestRunProviderUsageKeepsOpenCodeGoBearerAuthWithAnthropicModelDialect` pins it.
- `replaceProvider` (`internal/app/provider_lifecycle.go:5`) closes the superseded provider and runs
  from the Codex login and logout reloads (`internal/app/auth_commands.go:103`, `:154`); no test
  observes that close, and the login flow cannot complete under test.
- The two-picker chain after a TTY `/provider` (`internal/app/provider_commands.go:208`) runs
  only on Linux, via `TestRunTTYSlashProviderPickerChainsModelAndEffortSelection`
  (`internal/app/live_tty_linux_test.go:1599`).
