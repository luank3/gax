# Superseded: DeepSeek Model and Usage Hooks Plan

> Superseded on 2026-10-11. This plan followed the fork snapshot `23988f9`, not the porting baseline `ff458337f8ba7366d71fad4da0210a53b658678a`, which has no DeepSeek provider, custom model hook, or balance `/usage` command; the implementation was removed from the Go port at the time. DeepSeek's model parser and `/user/balance` reporting were later restored as a deliberate divergence from the baseline.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port DeepSeek's custom model metadata parser and `/usage` account-balance report.

**Architecture:** Keep DeepSeek response parsing and balance fetching in a provider package; pass the existing resolved runtime provider and transport client into the usage query. The REPL dispatches `/usage` by the selected provider's hook, with DeepSeek's report rendered as plain terminal-safe text.

**Tech Stack:** Go provider runtime, shared HTTP transport, REPL slash command.

**Spec:** hax `src/providers/deepseek.c`, `src/providers/usage_fetch.c`, `src/providers/usage_render.c`, and `tests/providers/test_deepseek.c`.

## Constraints

- Parse only positive integer context/output limits; keep unreported image/effort metadata unknown.
- Add `none` to a reported DeepSeek effort ladder even when the API omits it.
- Treat balances as numeric strings, skip malformed entries, cap results at four, and default an unreported availability flag to true.
- Strip terminal control characters from server-provided currency text before rendering.
- Keep the query cancellable, bounded to 30 seconds, and redact API keys from errors.

## Tasks

### 1. Parse DeepSeek model metadata

- [x] Write failing parser tests for the live and sparse `/models` entries.
- [x] Implement a typed parser and add the shipped `HookParseModel` flag.
- [x] Verify parser tests.

### 2. Parse, fetch, and format balances

- [x] Write failing balance parser/formatter and HTTP query tests.
- [x] Implement DeepSeek balance parsing, safe formatting, and `/user/balance` fetch.
- [x] Verify provider package tests.

### 3. Wire the `/usage` REPL command

- [x] Write failing command dispatch tests for DeepSeek and unsupported providers.
- [x] Dispatch DeepSeek usage through the resolved provider and shared transport.
- [x] Verify app and provider tests.

## Verification

- [x] Run `go test -race -count=1 ./...`.
- [x] Run `go vet ./...`.
- [x] Build Linux and macOS targets.

## Upstream interaction boundary

The Go runtime fetches OpenAI-style `/models` responses, applies the DeepSeek parser, and exposes the result in the line REPL. TTY model selection, Anthropic's cursor-paginated listing, and llama.cpp discovery are covered. The pinned upstream editor registers only slash-command-name and `@file` completers (`src/agent.c`); provider/model command arguments are not Tab-completed.
