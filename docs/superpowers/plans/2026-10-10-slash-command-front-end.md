# Slash Command Front-End Implementation Plan

> Upstream-parity correction (2026-10-12): the 2026-10-11 note below was wrong about upstream. The
> argument-completion extension was removed as "based on a fork snapshot", but `ff45833` is a commit
> on the fork's branch, not upstream master, and its ancestry predates upstream's own
> argument-taking commits. Upstream master (`2834c2c`) takes arguments for `/provider`, `/model`,
> and `/effort` and completes their values with Tab; `2026-10-12-slash-argument-completion.md` ports
> that behavior and supersedes the note.
>
> Superseded note (2026-10-11): this is a historical implementation plan. The argument-completion extension described in the original plan was based on a fork snapshot and has been removed. Official master `ff458337f8ba7366d71fad4da0210a53b658678a` completes slash command names only (plus `@file` in the editor); `/provider`, `/model`, and `/effort` take no arguments and open pickers.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port upstream slash-command recognition, command metadata, command-name completion, hints, and the useful line-REPL commands.

**Architecture:** Keep the command registry and grammar in a pure `internal/slash` package. The REPL consumes parsed outcomes and handles help and diagnostics without letting command-shaped input accidentally become a model prompt.

**Tech Stack:** Go standard library only.

**Spec:** hax `src/slash.c` and `tests/test_slash.c` at the porting baseline commit `ff458337f8ba7366d71fad4da0210a53b658678a`.

## Global Constraints

- Preserve the upstream command and alias order used by `/help` and completion.
- A slash-prefixed path or token containing punctuation/control bytes must remain normal prompt text.
- Unknown command names and invalid arguments are consumed with diagnostics.
- Do not add terminal UI dependencies in this slice.

## Review Focus

- Absolute paths such as `/tmp/a.c` remain prompts; covered by `TestParseSlashPathFallsThrough`.
- Control bytes in a command token do not reach terminal diagnostics; covered by `TestParseSlashControlBytesFallThrough`.
- Aliases participate in completion in registry order; covered by `TestSlashNameCandidatesAndCompletion`.
- Optional usage hints distinguish `/new` from `/new `; covered by `TestSlashHint`.
- Known but not yet implemented commands never reach the model as prompt text; covered by `TestRunREPLConsumesSlashCommands`.

---

### Task 1: Pure slash command grammar and metadata

**Files:**
- Create: `internal/slash/slash.go`
- Test: `internal/slash/slash_test.go`

**Interfaces:**
- Produces: `Commands() []Command`, `Parse(string) Parsed`, `CompleteName(string) (string, bool)`, `NameCandidates(string) (string, bool)`, `MatchName(string, int) (int, int, bool)`, and `Hint(string) (string, bool)`.
- `Parsed` carries an `Outcome`, command name, optional argument, and matched command metadata.

- [x] Write tests for command order, alias parsing, unknown names, bad usage, path/control fall-through, completion, candidate lists, cursor matching, and hints.
- [x] Run `go test ./internal/slash` and verify it fails because the package API is absent.
- [x] Implement the pure registry and parser with ASCII command-name bytes and upstream whitespace behavior.
- [x] Run `go test ./internal/slash` and verify all cases pass.

### Task 2: Line REPL command routing

**Files:**
- Modify: `internal/app/app.go`
- Test: `internal/app/app_test.go`

**Interfaces:**
- Consumes: `slash.Parse(string) Parsed` and `slash.Commands()`.
- Produces: `/help` in registry order, `/new` and `/clear` conversation reset with session-log rollover, and unknown-command/bad-usage diagnostics. Other recognized commands are consumed with an explicit unsupported-command diagnostic until their handlers are ported.

- [x] Add REPL tests proving `/help` does not invoke the provider, unknown and bad-usage commands are consumed, `/new` rolls history into a fresh log, and normal prose still reaches the provider.
- [x] Run the targeted app tests and verify the new cases fail against the current REPL.
- [x] Route parsed slash outcomes before model dispatch; render command metadata in `/help` order; reset conversation state and rotate the session log for `/new` and `/clear`.
- [x] Run `go test ./internal/app` and verify the command routing cases pass.

### Task 3: Undo and fork the active session

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/session/load.go`
- Create: `internal/session/fork.go`
- Test: `internal/app/app_test.go`
- Test: `internal/session/fork_test.go`

**Interfaces:**
- Consumes: `conversation.UserTurnCut`, `agent.Session.Retire`, and `session.Log.Undo`.
- Produces: `/undo N` retires the discarded live suffix and appends an undo record; `/fork N` creates an inherited branch file, switches the live logger, and clears source-only retired/runtime accounting. `/fork 0` clones the current tip.

- [x] Add failing tests for undo persistence/provider context, fork branch/source separation, fork-at-tip, invalid undo counts, and compact-seed turn counting.
- [x] Run the targeted tests and verify failure against the unsupported command handlers or absent fork API.
- [x] Implement undo routing and `session.ForkFile`, preserving source headers while changing branch id/time/selection and marking copied items inherited.
- [x] Run targeted app and session tests and verify live state and disk state agree.

### Task 4: Upstream slash-name completion

- [x] Match and complete slash command names at the cursor, with safe boundary handling.
- [x] Reject arguments for `/provider`, `/model`, and `/effort`; selection happens through their TTY pickers.
- [x] Preserve provider model-list order unless the provider's model-sort setting requests sorting.
- [x] Add parser regressions for no-argument commands and live TTY picker coverage.
- [x] Run focused tests for slash parsing, provider model caches, the app, and terminal input.

### Task 5: Full verification

- [x] Run repository tests, race checks, build, vet, formatting, and Linux/macOS build checks after parity changes.
- [x] Record completion of this slice in `.superpowers/sdd/2026-10-10-slash-command-front-end/progress.md`.

