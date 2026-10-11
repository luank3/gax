# Runnable command entry point

## Goal

Connect the existing CLI, config, provider, agent, tool, and session packages behind a runnable `gax` command with one-shot, basic interactive, resume, and JSONL modes.

## Tasks

- [x] Add loopback-server tests for provider requests, tool schemas, one-shot and interactive output, resume continuation, and JSONL records.
- [x] Implement `cmd/gax` and the application runner.
- [x] Run repository tests, race checks, build, vet, whitespace checks, and command smoke tests.
- [x] Port the full terminal UI, slash commands, session picker, live rendering, compaction, and complete usage/cost reporting.
  - [x] Render full history and brief recent-turn replay with prompt rails, Markdown, origin markers, and tool previews.
  - [x] Match upstream tool-header preprocessing and summarized-write preview fallbacks.
  - [x] Match the shipped provider inventory, including Openference and Agnes, without a DeepSeek provider.
  - [x] Parse Openference model metadata and preserve Agnes model listings without invented capabilities.
  - [x] Resolve dotted preset names literally across state and config tiers, including save metadata.
  - [x] Replay Chat Completions reasoning only under a configured, provider-default, or catalog-selected field; do not infer a request field from response-member names stored in the transcript.
  - [x] Match upstream slash-name completion and `@file` completion; `/provider`, `/model`, and `/effort` reject arguments and open their TTY pickers without arguments.
  - [x] Match Ollama's context guidance, with no default reasoning replay member.
  - [x] Carry OpenRouter pricing, context tiers, and tool support from model listings into usage estimates and picker rows.
  - [x] Probe selected OpenRouter, Anthropic, Codex, and llama.cpp metadata in the background; keep Openference's metadata in its explicit `/models` listing; use Codex's versioned catalog.
  - [x] Show merged context, image, pricing, and provider prose in model picker descriptions.
  - [x] Finalize background tasks and drain exit-note records before the final one-shot JSON result.
  - [x] Discard staged session selections before task-exit bookkeeping on shutdown, `/new`, and `/resume`.
  - [x] Include final context and exact or catalog-estimated spend in one-shot JSONL result records.

## Source

The porting baseline commit `ff458337f8ba7366d71fad4da0210a53b658678a` (pinned in `internal/version.Value`), including `src/main.c`, `src/cli.c`, `src/oneshot.c`, `src/agent.c`, `src/session.c`, `src/providers/registry.c`, and `docs/sessions.md`.
