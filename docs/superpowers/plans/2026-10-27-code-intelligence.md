# Code Intelligence Built-in Skill Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Equip gax with structure-aware code exploration capabilities via a built-in skill while preserving its standard-library-only, zero-dependency, minimalist identity.

**Architecture:**
- Ship a built-in `code-intelligence` skill inside the gax binary under `internal/skill/skills/code-intelligence/SKILL.md` (served via `gax:skills/code-intelligence/SKILL.md`). Teaches the agent AST search (`ast-grep`), tags (`ctags`), structural regex patterns, and git provenance tracking to navigate code without whole-file reads.

**Tech Stack:** Go standard library (`embed`, `io/fs`).

## Constraints

- Standard library only: `go.mod` has no `require` block and must keep none.
- Single binary, lean memory footprint.
- Built-in skills obey `no_skills`, `--bare`, and `--raw`.
- Tests must pass cleanly with `make fmt-check lint test-race`.

## Tasks

- [x] Create `internal/skill/skills/code-intelligence/SKILL.md`.
- [x] Harden skill recipes: Linux `ast-grep` command check before `sg` (shadow-utils conflict), `ast-grep scan --inline-rules` for Go function/method declarations, tab-delimited ctags matching via `awk`, co-changed files git pipeline, test inclusion in caller search, tightened Rust item/impl regexes and multiline C/C++ regexes, file outline recipes, and sorted hotspot frequencies.
- [x] Update `internal/tools/skill_test.go` and `internal/skill/discover_test.go` to assert the built-in skill listing, loading, and shadowing.
- [x] Update `README.md` and `CHANGELOG.md` to document the two built-in skills (`code-intelligence` and `customize-gax`).
- [x] Verify with `make fmt-check lint test-race`.
