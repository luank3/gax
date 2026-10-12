# Slash Argument Completion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement command-argument handling for `/provider`, `/model`, and `/effort`: their
`[id]`/`[level]` prompt placeholders, the values they accept as an argument, and Tab completion of
the values every command that enumerates them offers.

**Architecture:** `internal/slash` keeps the registry and grammar: a command carries the
`Usage` placeholder it already had plus `ArgumentChoices`, which marks the commands whose first
argument word the completer owns. The completer itself mirrors the reference's `match`/`complete`/
`candidates` triple in the editor's existing `Completer` shape — the matched span is the text after
the slash, so name and argument completion share one code path. `internal/app/slash_completion.go`
supplies the per-command candidate lists from the live selection and adapts them to the editor.

**Tech Stack:** Go standard library only.

**Spec:** the ported command surface gains argument handling: the commands that enumerate values
(`/provider`, `/model`, `/effort`, `/preset`, `/new`, `/clear`) own the first argument word, their
placeholders already say so (`usage = "[id]"`, `"[level]"`), and completion of that word extends the
same match/complete/candidates machinery the command-name completer already uses. The per-command
candidate lists come from the live selection: provider ids, model ids, effort levels, and preset
names.

## Constraints

- Registry order, usage strings, and diagnostics follow the ported behavior: `/provider [id]`, `/model [id]`,
  `/effort [level]`.
- A command that enumerates its argument owns that word. Every other argument word stays with the
  completers that follow, so `@file` mentions inside a `/compact` focus keep working.
- Candidates come from memory only: a Tab never fetches a listing, a catalog, or a network
  response. Model ids therefore complete from the provider's last listing (the `/model` picker's
  fetch), and nothing is offered before one exists.
- Only the first argument word enumerates; a later word is left to the other completers.
- The listing shows only what follows the typed word's last slash, like a shell listing a
  directory, and a bare `/` still points at `/help`.
- Completion applies on the line's first row only, so a pasted multi-line buffer is left alone.

## Tasks

### 1. Registry metadata and placeholders

- [x] Add `Usage` for `/provider` (`[id]`), `/model` (`[id]`), and `/effort` (`[level]`).
- [x] Add `Command.ArgumentChoices` and set it for `/new`, `/preset`, `/provider`, `/model`, and
  `/effort`.
- [x] Verify `internal/slash` hint tests cover the new placeholders.

### 2. Argument acceptance

- [x] Confirm the handlers' argument paths (`runProviderCommand`, `runModelCommand`,
  `runEffortCommand`) are reachable now that the parser accepts an argument, and that a missing or
  unknown value keeps its documented diagnostic on the display channel.
- [x] Verify with `internal/app` tests: `/provider <id>`, `/model <id>`, `/effort <level>` switch,
  `/provider missing` names the unknown provider, and a level the model does not take is reported.
- [x] Verify `@file` completion is unaffected after a command that enumerates nothing.

### 3. Completer

- [x] Replace `MatchName`/`CompleteName`/`NameCandidates` with `Match`/`Complete`/`ListCandidates`,
  which take the argument candidates the caller supplies.
- [x] Port the shared-prefix extension, the candidate filter, and the last-slash listing trim.
- [x] Verify `internal/slash` tests for names, aliases, ambiguous prefixes, and each argument case.
- [x] Verify editor tests: a unique argument completes with a trailing space, a repeated Tab lists
  the values, and the command text before the word is preserved.

### 4. Candidates from the live selection

- [x] Add `internal/app/slash_completion.go`: sorted provider ids, the current provider's listed
  model ids (version-aware order when the provider sorts models), the current model's effort levels
  followed by `default`, and the preset names in the preset-name grammar.
- [x] Wire the candidates through `runREPL`/`runTTYREPL` from the `Run` closure that owns the live
  provider and model.
- [x] Verify with an `internal/app` unit test per command and through the editor.

### 5. Documentation

- [x] Update `README.md` (the slash-command table row and a completion paragraph) and
  `CHANGELOG.md`.
- [x] Correct the parity note in `2026-10-10-slash-command-front-end.md`.
- [x] Run `make lint test-race`.

## Verification

- [x] `make lint`.
- [x] `make test-race`.
- [x] `/provider`, `/model`, and `/effort` in a real REPL show their placeholders and complete
  their values on Tab; `/model openai/` lists the ids that follow the shared `openai/` prefix.

## Record

- **The note that removed this extension was wrong.** `2026-10-10-slash-command-front-end.md`
  recorded that slash commands complete their names only and that `/provider`, `/model`, and
  `/effort` take no arguments. That was read off a revision that predates the argument-taking
  behavior, which already existed when the note was written. gax had kept the handlers' argument
  paths (`runProviderCommand`, `runModelCommand`, `runEffortCommand`) — dead code, since the
  registry declared no usage and the parser rejected the argument — and this change makes them
  live, which is where the summary text "switch provider, then model and effort" comes from.
- **One completer, not three.** The reference's inside-the-package `match`/`complete`/`candidates` and
  gax's `terminalinput.Completer` fields already line up, so `internal/slash` gains no new wiring:
  `Match` returns the span from after the slash through the cursor and `Complete` returns its
  replacement, exactly as the name-only version did.
- **`Command.ArgumentChoices` is the `argument_choices` hook, without the function value.**
  The package cannot know a registry or a store, so it keeps only the flag that decides who owns the
  argument word, and the app passes the values in.
- **`/new` and `/clear` share the preset candidates**, as `find_command` does.
- **Effort candidates always end with `default`**, even when the model lists no levels, matching
  `select_effort_choices`; the candidate itself is not filtered, so a provider with no levels still
  offers the value `/effort` accepts.
- **Preset candidates are alphabetical and grammar-checked.** They are sorted
  (`completion_sort`) and drops names that cannot be one word, so `/preset code review<Tab>` offers
  nothing rather than a partial name.
- **Model candidates need one listing first.** The probes report ids as a side effect of
  metadata work; gax has no listing-only probe, so completion uses
  `Provider.ListedModelIDs()` — populated by `ListModels`, which is what the `/model` picker and
  `/provider <id>` run. The same fallback is documented ("open the `/model`
  picker once").
- **Not ported:** the `?q=` OpenRouter search probe and the listing-only probes (which exist to
  make `/model` complete without the picker ever running). The `text/display_safe` sanitizing of echoed candidates is applied where gax
  builds them (`terminalSafeText`), next to the other terminal-bound values.
