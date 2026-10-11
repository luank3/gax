# Skill Tool Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move skills out of the system prompt: a `skill` tool lists what a session can load and
returns the document on request, so skill descriptions and paths are only sent when the model asks
for them.

**Architecture:** `internal/skill` owns discovery (project roots up to the git worktree root, the
gax config directory, the shared `~/.agents/skills` root, then the embedded documents) and loading;
`internal/tools/skill.go` exposes it as a tool whose no-argument form is the listing and whose
`name` argument loads a document. The prompt's `# Skills` section and its helpers are deleted.

**Tech Stack:** Go standard library, the existing tool registry, config store, and system path/fs
helpers.

**Spec:** upstream hax lists skills in the system prompt (`src/agent_env.c: collect_skills`), so
this is a deliberate gax divergence, like the built-in `customize-gax` skill it applies to. The
trade is explicit: the listing costs prompt tokens on every request, and the model only needs it
when a task matches.

## Constraints

- Discovery keeps its precedence: nearest project root first, then the gax config directory, then
  the shared root, with the embedded documents as the fallback a discovered skill shadows.
- A shared-root symlink in a project is still skipped, so the same skill is not listed twice.
- The prompt no longer mentions skills at all: no `# Skills` section, no description, no path.
- `no_skills` disables the tool (and refuses a stale call); `--bare` sets it, so its documented
  "no skills" meaning survives the move; `--raw` still sends no tools.
- The listing keeps the shape the prompt used (`- name: description (path)`), and a loaded document
  is returned whole, unnumbered, and uncapped — it is instructions, not a file to edit.
- Loading reads the file at that moment; a document that vanished or is larger than 1 MiB is an
  error the model can see, not a silent empty result.

## Tasks

### 1. Move discovery into `internal/skill`

- [x] Move the frontmatter parser and its tests from `internal/agent`.
- [x] Write discovery tests for precedence, shadowing, the shared-root symlink, dot directories,
      missing documents, and loading.
- [x] Add `Discover`, `Entry`, and `Entry.Load`.

### 2. Strip the prompt section

- [x] Replace the skill prompt tests with one asserting the prompt carries no skill text.
- [x] Delete `buildSkills`/`collectSkills`/`skillDescription` and the `# Skills` gate.

### 3. Add the tool

- [x] Write tool tests for listing, loading (project and embedded), unknown names, argument errors,
      the `no_skills` switch, definitions, and presentation.
- [x] Add `internal/tools/skill.go` and register it after the web tools.
- [x] Verify `internal/tools` tests.

### 4. Keep `--bare` honest

- [x] Write a failing app test asserting a bare one-shot run is not offered the skill tool.
- [x] Set the `no_skills` run override before the tool list is collected.
- [x] Update the `--bare` help text and the `no_skills` description.

### 5. Document the move

- [x] Update the README tools paragraph and built-in skill paragraph, the changelog, AGENTS.md, and
      the `customize-gax` document's prompt-section notes.
- [x] Run `make lint test-race`.

## Verification

- [x] Run `make lint`.
- [x] Run `make test-race`.
- [x] Cross-build linux/darwin amd64+arm64 with `CGO_ENABLED=0`.
- [x] Smoke test: a scripted mock session in a project with one discovered skill lists both it and
      the built-in, then loads the built-in document.
- [x] Transcript check: the system prompt sections are Background tasks, Subagents, Environment —
      no skills section — while the listing and the loaded document appear as tool results.

## Record

- `internal/skill` now owns both halves of a skill's life: the embedded documents (listed and
  served under `gax:` for the read tool) and the discovery walk that used to live in the prompt
  builder. `internal/agent` no longer imports it at all.
- The listing moved verbatim, so the model sees the same lines it used to read in the prompt, now
  only after asking for them.
- The loaded document is not line-numbered: `read` numbers files because its output is a map of the
  file, while a skill is instructions to follow. `read` on a `gax:` path still numbers the same
  document.
- `--bare` needed the override because the tool list is collected before the prompt is built; the
  override now runs first, and `/config` reports `no_skills = on (run)` for such a run.
- A skill directory whose `SKILL.md` cannot be read for its frontmatter is not listed at all, so the
  tool's load error only fires for a document that disappears between the listing and the load.

## Upstream divergence

Upstream keeps a `# Skills` section in the system prompt and has no skill tool. gax now sends no
skill text until the model asks, which is the same trade the built-in `customize-gax` skill already
made for its own content: the registry-driven settings table is only paid for when the document is
loaded.
