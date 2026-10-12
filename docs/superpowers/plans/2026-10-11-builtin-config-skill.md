# Built-in Configuration Skill Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a skill inside the gax binary that teaches the model how to change gax's own
configuration (settings, providers, presets, prompt sections), without writing anything into the
user's home directory.

**Architecture:** A new `internal/skill` package embeds `skills/<name>/SKILL.md` documents and
serves them under the `gax:` path scheme; `internal/agent` lists them after discovered skills (so a
discovered skill with the same name shadows the built-in), and `internal/tools` serves their bodies
from the `read` tool. The settings table inside the document is generated from
`config.DefaultSettings()` at load time, so it cannot drift from the registry.

**Tech Stack:** Go standard library (`embed`, `io/fs`), the existing system-prompt builder and read
tool.

**Spec:** this is a deliberate gax addition — discovery lists only skills found on disk. The skill
*content* is drawn from gax's own `README.md` and `docs/configuration.md`.

## Constraints

- No third-party modules; `go:embed` and `io/fs` are standard library.
- No filesystem writes: the built-in skill is never installed into `~/.config/gax/skills/`, because
  tests and one-shot runs must not touch the user's configuration directory.
- The built-in listing obeys `no_skills`, `--bare`, and `--raw` exactly as discovered skills do.
- A discovered skill named like a built-in shadows it (built-ins are the fallback).
- The settings table lists every registered setting exactly once, with its environment variable,
  default, whether `/config` can change it live, and its description.
- The read tool serves built-in paths without consulting the filesystem, and reports an unknown
  `gax:` path as a built-in lookup failure rather than an OS error.

## Tasks

### 1. Embed built-in skills and serve them

- [x] Write failing tests for the embedded document, its path scheme, and the generated table.
- [x] Add `internal/skill` with `Builtin()`, `Read()`, and the settings-table splice.
- [x] Verify `internal/skill` tests.

### 2. List built-ins in the system prompt

- [x] Write failing prompt tests for the listing entry and for shadowing by a discovered skill.
- [x] Seed the skills map with built-ins after collecting discovered roots.
- [x] Verify agent tests.

### 3. Serve built-in skills through the read tool

- [x] Write failing read-tool tests for a built-in path and for an unknown `gax:` path.
- [x] Resolve `gax:` paths before path expansion and stat.
- [x] Verify tools tests.

### 4. Document the feature

- [x] Add the skill to the README configuration table and note the path scheme.
- [x] Run `make lint test-race`.

## Verification

- [x] Run `make lint`.
- [x] Run `make test-race` (51 packages).
- [x] Confirm no test writes to `~/.config/gax/` (it does not exist after the run; nothing in the
      change writes files).

## Record

- The rendered document is 16 555 bytes / 201 lines, of which the generated settings table is 68
  rows. `read` returns it numbered like a file (`     1→---`), and the tool cap is deliberately not
  applied: the document is fixed-size and the tail is the reference table.
- The listing line is `- customize-gax: Change gax's own settings, providers, models, or presets —
  where config.json lives, which keys exist, what applies live, and what needs a restart.`, sorted
  among discovered skills; the path moved to the `skill` tool's own listing
  (2026-10-11-skill-tool.md). `no_skills`, `--bare`, and `--raw` drop both.
- `internal/skill` is the first use of `go:embed` in the port; `go.mod` still has no `require` block.

## Interaction with the ported behavior

Discovery reads skills from disk only and ships none.
The `gax:` scheme is a gax-only extension: discovered skills keep their filesystem paths, and the
`# Skills` section wording is unchanged, so user skills read exactly as they did before.
