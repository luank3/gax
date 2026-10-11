# Line REPL Model Selection Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the line-oriented REPL's `/model` command use the provider model-list backend and switch the live conversation to a requested model.

**Architecture:** `/model` without an argument lists the selected provider's model IDs; `/model <id>` updates the conversation-level selection, session snapshot, displayed model state, and the model used by subsequent requests.

**Tech Stack:** Slash command parser, app REPL dispatch, provider runtime model listing, session selection records.

**Spec:** hax `src/slash.c`, `src/select.c`, and `docs/usage.md`.

## Constraints

- Keep model IDs exactly as returned by the provider.
- Do not require a model-list request for an explicit `/model <id>` switch.
- Preserve the currently selected effort and provider when switching models.
- Make `/model` without an argument usable in a non-TTY REPL by printing candidates.
- Persist the changed model as a conversation selection record.

## Tasks

- [x] Add failing parser and app tests for list, switch, and next-request behavior.
- [x] Accept the optional model ID and dispatch both forms from the REPL.
- [x] Persist the model selection and list model IDs through `Provider.ListModels`.
- [x] Add the TTY fuzzy picker and clamp requests to model-supported effort levels.
- [x] Run full tests, vet, and Linux/macOS builds.

## Upstream interaction boundary

The current upstream completes `/model` arguments from the most recently listed model IDs. Go keeps those IDs in provider order and applies the model-sort setting for completion just as it does for the picker.
