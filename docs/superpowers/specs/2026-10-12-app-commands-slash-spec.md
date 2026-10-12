# slash command front-end spec

## Scope

The registry, line classifier, Tab completion, ghost hint, and picker selection behind gax's slash
commands, in `internal/slash`, `internal/app/slash_completion.go`, `internal/app/model_picker.go`
and `internal/terminal/picker`. Inputs: a prompt line, a cursor offset, the live provider, model and
`*config.Store`. Outputs: a `slash.Parsed`, editor text, picker indexes. Non-goal: command handlers.

## Invariants

### One registry owns order, names, and usage
- **Rule:** Keep `commandRegistry` in upstream order and expose it only through `Commands()`, which
  returns a copy, since that order drives `/help` rows and completion candidates.
- **Source:** `internal/slash/slash.go:39`: `commandRegistry`; copy in `Commands` at :61.
- **Assertion:** `TestCommandsMatchUpstreamOrder` in `internal/slash/slash_test.go:8`: names, two
  summaries, aliases, and order against a literal list.
- **Rejected:** Sorting the registry for display; the test compares `new/clear, resume, undo, ...`.

---
### A slash-prefixed non-command stays prompt text
- **Rule:** Call a line a command when `/` is followed by one or more `A-Za-z0-9_-` bytes and then a
  space or line end, so `/tmp/file.go inspect this`, `/foo.bar`, and `/\x1b[2J` stay prompts.
- **Source:** `internal/slash/slash.go:84`: `parseCommand`; byte checks at :90 and :95.
- **Assertion:** `TestParseSlashOutcomes` in `internal/slash/slash_test.go:43`, rows :56 to :58: all
  three parse as `NotCommand` with a nil `Command`.
- **Rejected:** Naming the first whitespace-separated field; the table wants `NotCommand` there.

---
### Unknown names and bad usage are consumed, not run
- **Rule:** Consume `slash.Unknown` and `slash.BadUsage` lines without a turn, printing
  `unknown command: /%s. type /help for the list.` or `/%s takes no arguments.` on stdout and
  nothing on stderr.
- **Source:** `internal/slash/slash.go:76`: the `Usage == ""` guard in `Parse`; diagnostics in
  `internal/app/history_commands.go:18` at :23 and :25.
- **Assertion:** `TestRunREPLConsumesSlashCommands` in `internal/app/app_test.go:2064` sees one
  provider request for `/help`, `/nonesuch`, `/model fast`, `/resume`; `BadUsage` appears in
  `TestRunREPLSelectionErrorsUseUpstreamDisplayChannel` (`internal/app/app_test.go:1130`).
- **Rejected:** Passing unknown commands to the model; the typo would cost a turn.

---
### An alias resolves to its canonical command
- **Rule:** Match `Command.Alias` in `findCommand` while keeping `Parsed.Name` as typed, so `/clear`
  dispatches as `new` and shares its `[preset]` usage.
- **Source:** `internal/slash/slash.go:113`: `findCommand`; copy into `Parsed.Command` at :74.
- **Assertion:** `TestSlashHint` in `internal/slash/slash_test.go:209`: `/clear` hints ` [preset]`.
- **Rejected:** Returning the alias as `Command.Name`; the REPL dispatches on `parsed.Command.Name`.

---
### Only an enumerating command owns its argument word
- **Rule:** Give the command completer the first argument word only when `ArgumentChoices` is set;
  other words stay with the following completers, so `/compact @src` keeps mention completion.
- **Source:** `internal/slash/slash.go:131`: `Match`, guard at :148, `commandChoices` at :201.
- **Assertion:** `TestMatchSlashCommandTextAtCursor` in `internal/slash/slash_test.go:174`:
  `/compact @src` at cursor 13 does not match.
- **Rejected:** Gating on `Usage != ""`; `/compact` declares `[focus]` but owns no choices.

---
### Tab completes, extends, or lists
- **Rule:** Replace the text after the slash through the cursor: a unique candidate gains a trailing
  space, several extend only to their shared prefix, and a repeated Tab lists them on one
  two-space-indented row trimmed past the typed word's last `/`.
- **Source:** `internal/slash/slash.go:158`: `Complete`; `extendCandidates` at :247,
  `ListCandidates` at :169.
- **Assertion:** `TestSlashCompleteNamesAndAliases` in `internal/slash/slash_test.go:105`: `mo`
  gives `model `, `pre` gives `preset`, `c` gives nothing; `TestSlashCompleteSelectionArguments` in
  `internal/slash/slash_test.go:132` lists `  gpt-5 gpt-5-mini` for `model openai/`.
- **Rejected:** Completing the first of several matches; `c` would become `clear `.

---
### Candidates come from memory and are made terminal-safe
- **Rule:** Build each list from state already in memory, model ids from the provider's last
  listing, and pass every candidate through `terminalSafeText` before the editor echoes it.
- **Source:** `internal/app/slash_completion.go:27`: `slashArgumentChoices`; loop at :45,
  `modelIDChoices` at :63.
- **Assertion:** `TestSlashArgumentChoicesForSelection` in
  `internal/app/slash_completion_test.go:64`: `/model` offers nothing until `ListModels` runs, then
  `alpha-model`, `esc[31m`, `zeta-model`.
- **Rejected:** Fetching inside the completer; the ids reach the prompt sanitized.

---
### Pickers match every query term case-insensitively
- **Rule:** Match an item when every space-separated query term appears in its label, folding ASCII
  case, so padded and mis-cased queries still match.
- **Source:** `internal/terminal/picker/core.go:60`: `Match`; `containsFoldASCII` at :80.
- **Assertion:** `TestMatchRequiresEverySpaceSeparatedCaseInsensitiveTerm` in
  `internal/terminal/picker/core_test.go:5`: `OPUS claude` matches `anthropic/claude-opus-4`.
- **Rejected:** Testing the whole query as one substring; the `  hello   world  ` case then fails.

---
### The model picker sorts only when the provider asks
- **Rule:** Reorder picker models with `modelsort.CompareIDs` only when `provider.SortModels()` is
  true, sorting a copy rather than the caller's slice, and mark a row `Dim` with `Detail: "no tool
  calling"` only when `Tools` is `catalog.SupportNo`.
- **Source:** `internal/app/model_picker.go:14`: `modelsForPicker`; `modelPickerItem` at :25.
- **Assertion:** `TestModelsForPickerUsesVersionOrderOnlyWhenConfigured` in
  `internal/app/app_test.go:653` keeps `gpt-5-mini, gpt-5-2025-08-07, gpt-5, gpt-5.6` for
  `sort_models:"off"`; `TestModelPickerMarksModelsWithoutToolSupport` at :706.
- **Rejected:** Sorting in place, or dimming `SupportUnknown`; the tests forbid both.

---
### A hint shows only a command awaiting its first argument
- **Rule:** Return `command.Usage` for a complete command name with no argument yet, prefixing a
  space only when the name is the whole line, so `/new` hints ` [preset]` and `/new ` hints
  `[preset]`.
- **Source:** `internal/slash/slash.go:282`: `Hint`; prefix at :302, `Usage == ""` guard at :291.
- **Assertion:** `TestSlashHint` in `internal/slash/slash_test.go:209`: `/new`, `/clear`, and
  `/model ` hint; `/mo`, `/model foo`, and `/new\n` give nothing.
- **Rejected:** Hinting a partial name; `/mo` would advertise `model`'s `[id]`.

### Only name-byte text is a command
- **Rule:** Treat a line as a command only when `/` is followed by one or more `A-Za-z0-9_-` bytes, so
  `/tmp/file.go inspect this`, `/foo.bar`, and `/\x1b[2J` stay prompt text with no `Command`.
- **Source:** `internal/slash/slash.go:84`: `parseCommand`; name-byte check at :90, empty name at :95.
- **Assertion:** `TestParseSlashOutcomes` in `internal/slash/slash_test.go:43`, the three
  `NotCommand` rows at :56 to :58.
- **Rejected:** Naming the first whitespace-separated field; a pasted path would never reach the model.

---
### A no-usage command never reaches the model
- **Rule:** When `HasArgument` holds and the matched entry has `Usage == ""`, set `BadUsage` but keep
  the entry on the result, so the REPL answers `/%s takes no arguments.`
- **Source:** `internal/slash/slash.go:76`: the guard in `Parse`; diagnostic at
  `internal/app/history_commands.go:25`.
- **Assertion:** `TestParseSlashOutcomes` in `internal/slash/slash_test.go:43`: rows :60 and :61.
- **Rejected:** Reporting a bad argument as an unknown name; those rows keep the matched entry.

---
### Model rows flag missing tool calling
- **Rule:** Mark a model row as lacking tool calling only for `catalog.SupportNo`; leave
  `catalog.SupportUnknown` neutral.
- **Source:** `internal/app/model_picker.go:25`: `modelPickerItem`, guard at :29.
- **Assertion:** `TestModelPickerMarksModelsWithoutToolSupport` in `internal/app/app_test.go:706`.
- **Rejected:** Dimming unknown support; unknown means the catalog holds no opinion.

---
### A hint shows only a command awaiting its first argument
- **Rule:** Return the registry placeholder for a complete command name with no argument yet,
  prefixed with a space when the name is the whole line (` [preset]`), and nothing for `/mo`,
  `/zzz`, `/model foo`, or a line holding `\n`.
- **Source:** `internal/slash/slash.go:282`: `Hint`; prefix decision at :302.
- **Assertion:** `TestSlashHint` in `internal/slash/slash_test.go:209`: `/new` gives ` [preset]`,
  `/new ` gives `[preset]`, and `/mo` and `/model foo` give nothing.
- **Rejected:** Always prefixing a space; `/new ` would then show ` [preset]`.

---

## Gaps

- `sessionPickerMaxEntries` (200) and the `resume a conversation · newest %d of %d` title
  (`internal/app/session_picker.go:43`, `:60`) have no fixture beyond
  `TestRunTTYSlashResumeSwitchesConversationAndExcludesCurrent`
  (`internal/app/live_tty_linux_test.go:1191`) covering exclusion.
- `TruncateQuery`'s UTF-8 walk (`internal/terminal/picker/core.go:228`) is unexercised:
  `TestCoreQueryNavigationAndViewportFollowMatches` (`internal/terminal/picker/core_test.go:24`)
  truncates the ASCII query `ALPHA` only.
- The hint's editor wiring (`internal/app/repl.go:143`) is untested; `TestSlashHint`
  (`internal/slash/slash_test.go:209`) covers `slash.Hint` alone, not the ghost line.
- Plan text disagrees: `docs/superpowers/plans/2026-10-10-slash-command-front-end.md:31` to `:33`
  cite `TestParseSlashPathFallsThrough`, `TestParseSlashControlBytesFallThrough`, and
  `TestSlashNameCandidatesAndCompletion`, none of which exists in `internal/slash`.
- `effortLevelChoices` appends `default` (`internal/app/slash_completion.go:86`), and
  `TestSlashArgumentChoicesForSelection` checks only that it is last, so a provider already
  reporting `default` would duplicate it unseen.

## Review

- Two tables pin the shared completion path: `internal/slash/slash_test.go:105` (names, aliases,
  bare `/`) and `:132` (argument words, last-slash trim). The "first argument word only" guard at
  `internal/slash/slash.go:222` is what makes `effort high ` list nothing.
- `internal/app/history_commands.go:35` and `:42` print `not available in the line-oriented
  interface yet`, but every registry command has a branch before the fallback at
  `internal/app/app.go:1091`; both look unreachable.
- `Match` returns a span after the slash (`internal/slash/slash.go:152`), so `Complete` and
  `ListCandidates` receive the line without its `/`; a caller passing the raw line gets a bad span.
- `sharedPrefix`'s `utf8.RuneStart` backoff (`internal/slash/slash.go:275`) is not reached by any
  test: every candidate and typed word in the tables is ASCII.
