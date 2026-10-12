# Line REPL Provider Selection Plan

> Upstream-parity correction (2026-10-12): the 2026-10-11 note below was wrong about upstream.
> `ff45833` is a commit on the fork's branch, not upstream master, and its ancestry predates
> upstream's own argument-taking commits. Upstream master (`2834c2c`) declares `usage = "[id]"`
> (`"[level]"` for `/effort`) with an `argument_choices` hook in `src/slash.c`, so it takes those
> arguments and completes them with Tab; `2026-10-12-slash-argument-completion.md` ports that
> behavior and supersedes the note below.
> Superseded on 2026-10-11: the optional `/provider [id]` and `/model [id]` line-command behavior in this historical plan came from a fork snapshot. Official master `ff458337f8ba7366d71fad4da0210a53b658678a` accepts no arguments for `/provider`, `/model`, or `/effort`; those commands use TTY pickers.

> **For agentic workers:** REQUIRED-SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Historical plan for provider/model selection before the first prompt; see the superseding upstream-parity note above.

**Architecture:** `/provider` lists visible registry IDs; `/provider <id>` resolves and switches the live provider, using provider defaults or llama.cpp server discovery when available and otherwise clearing the current model. A missing model is allowed in the REPL but remains an error for one-shot requests; prompt lines without a model print guidance and leave the REPL available for `/model`.

**Tech Stack:** Slash parser, provider registry/runtime, line REPL, session selection records.

**Spec:** hax `src/slash.c`, `src/select.c`, and `docs/usage.md`.

## Tasks

- [x] Add failing tests for optional `/provider` arguments, provider listing, provider switching, no-model startup, and later request routing.
- [x] Add provider command listing/switching and clear model state on provider changes.
- [x] Allow model-less REPL startup and preserve one-shot validation.
- [x] Add an availability-aware TTY provider picker with transactional model/effort selection while retaining list output for non-TTY input.
- [x] Probe local providers during automatic selection and load Codex model/effort defaults.
- [x] Discover and reconcile llama.cpp models at startup, resume, preset application, and direct provider switches; keep discovered choices run-scoped.
- [x] Run full tests, vet, and Linux/macOS builds.

## Upstream interaction boundary

The source cited when this plan was written was a fork snapshot. Official upstream registers slash-name completion only and does not complete provider, model, or effort arguments.
