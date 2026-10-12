# Line REPL Provider Selection Plan

> Correction (2026-10-12): the 2026-10-11 note below was wrong. `/provider` and `/model` declare
> `usage = "[id]"` (`"[level]"` for `/effort`) with an `argument_choices` hook, so they do take
> those arguments and complete them with Tab; `2026-10-12-slash-argument-completion.md` ports that
> behavior and supersedes the note below.
>
> Superseded on 2026-10-11: the optional `/provider [id]` and `/model [id]` line-command behavior in
> this historical plan was removed at the time, on the mistaken belief that those commands accept no
> arguments and use TTY pickers only.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Historical plan for provider/model selection before the first prompt; see the correction at the top.

**Architecture:** `/provider` lists visible registry IDs; `/provider <id>` resolves and switches the live provider, using provider defaults or llama.cpp server discovery when available and otherwise clearing the current model. A missing model is allowed in the REPL but remains an error for one-shot requests; prompt lines without a model print guidance and leave the REPL available for `/model`.

**Tech Stack:** Slash parser, provider registry/runtime, line REPL, session selection records.

**Spec:** the ported sources, and `docs/usage.md`.

## Tasks

- [x] Add failing tests for optional `/provider` arguments, provider listing, provider switching, no-model startup, and later request routing.
- [x] Add provider command listing/switching and clear model state on provider changes.
- [x] Allow model-less REPL startup and preserve one-shot validation.
- [x] Add an availability-aware TTY provider picker with transactional model/effort selection while retaining list output for non-TTY input.
- [x] Probe local providers during automatic selection and load Codex model/effort defaults.
- [x] Discover and reconcile llama.cpp models at startup, resume, preset application, and direct provider switches; keep discovered choices run-scoped.
- [x] Run full tests, vet, and Linux/macOS builds.

## Interaction with the ported behavior

The ported editor registers slash-name completion only, and does not complete provider, model, or
effort arguments.
