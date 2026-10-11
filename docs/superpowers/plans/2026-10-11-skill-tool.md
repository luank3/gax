# Skill Tool Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut the document path out of the skill listing in the system prompt and add a `skill`
tool that resolves a skill by name, so the model never composes a path to read one.

**Architecture:** `internal/skill` owns discovery (project roots up to the git worktree root, the
gax config directory, the shared `~/.agents/skills` root, then the embedded documents) and loading;
`internal/agent` renders the listing from that discovery as `- name: description`, and
`internal/tools/skill.go` loads by name (its no-argument form lists the same skills *with* paths, for
the model that wants to read a file beside a document).

**Tech Stack:** Go standard library, the existing tool registry, config store, and system path/fs
helpers.

**Spec:** upstream hax lists skills in the system prompt with their paths and has no skill tool
(`src/agent_env.c: collect_skills`), so both halves of this change are deliberate gax divergences,
like the built-in `customize-gax` skill they apply to. What the prompt keeps — names and
descriptions — is what the model needs to *choose*; what it drops — the path — is what the tool can
produce on demand, and the model would only echo it back into a `read` call.

## Constraints

- Discovery keeps its precedence: nearest project root first, then the gax config directory, then
  the shared root, with the embedded documents as the fallback a discovered skill shadows.
- A shared-root symlink in a project is still skipped, so the same skill is not listed twice.
- The prompt listing carries `- name: description` (or `- name` without frontmatter) and no path
  text at all: not `SKILL.md`, not `gax:skills/`, not a directory.
- The tool loads by name, for discovered and embedded skills alike; a name it does not know is an
  error that lists what is available.
- `no_skills` drops the listing and the tool (a stale call is refused); `--bare` sets it, so its
  documented "no skills" meaning covers both; `--raw` still sends no prompt and no tools.
- A loaded document is returned whole, unnumbered, and uncapped — it is instructions, not a file to
  edit. Loading reads the file at that moment; a document that vanished, or is larger than 1 MiB,
  is an error the model can see, not a silent empty result.

## Tasks

### 1. Move discovery into `internal/skill`

- [x] Move the frontmatter parser and its tests from `internal/agent`.
- [x] Write discovery tests for precedence, shadowing, the shared-root symlink, dot directories,
      missing documents, and loading.
- [x] Add `Discover`, `Entry`, and `Entry.Load`.

### 2. List skills without paths

- [x] Write prompt tests asserting the listing carries names and descriptions, no path text, and
      that a discovered skill still shadows a built-in.
- [x] Render the section from `skill.Discover`, and point it at the tool.

### 3. Add the tool

- [x] Write tool tests for loading (project and embedded), listing with paths, unknown names,
      argument errors, the `no_skills` switch, definitions, and presentation.
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
      the built-in, then loads the built-in document by name.
- [x] Transcript check: the prompt's skill section reads
      `- customize-gax: Change gax's own settings, …` with no path, and the loaded document appears
      as the tool result.

## Record

- `internal/skill` owns both halves of a skill's life: the embedded documents (served under `gax:`
  for the read tool) and the discovery walk that used to live in the prompt builder. Both the prompt
  listing and the tool render from it, so precedence cannot drift between them.
- The prompt listing is the same line as before minus the parenthesized path, and the section header
  now names the tool: `Load a skill with the \`skill\` tool when a task matches its description:`.
- The tool's no-argument listing keeps the path, which is the point: the prompt no longer spends
  tokens on a path per skill, and the model can still get one when it needs to read a file beside a
  document.
- The loaded document is not line-numbered: `read` numbers files because its output is a map of the
  file, while a skill is instructions to follow. `read` on a `gax:` path still numbers the same
  document.
- `--bare` needed the override because the tool list is collected before the prompt is built; the
  override now runs first, and `/config` reports `no_skills = on (run)` for such a run.
- A skill directory whose `SKILL.md` cannot be read for its frontmatter is not listed at all, so the
  tool's load error only fires for a document that disappears between the listing and the load.

## Upstream divergence

Upstream keeps paths in the prompt listing and has no skill tool. gax keeps the listing's choosing
information (name, description) and moves the addressing information (path) behind a tool call,
which is the same trade the built-in `customize-gax` skill already made for its own content: the
registry-driven settings table is only paid for when the document is loaded.
