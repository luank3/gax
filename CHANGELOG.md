# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Initial Go reimplementation of [hax](https://github.com/OleksandrChekhovskyi/hax), tracking the
baseline commit `ff45833`.

### Added

- Interactive REPL, one-shot (`-p`) mode, NDJSON output (`--json`), and resume/fork/undo over
  per-directory session logs.
- Streaming provider clients for the OpenAI Responses, OpenAI Chat Completions, and Anthropic
  Messages wires, with retries, idle timeouts, and redacted wire traces.
- Shipped providers: Codex (ChatGPT login), OpenAI, Anthropic, OpenRouter, OpenCode Go,
  DeepSeek, llama.cpp, Ollama, plus generic OpenAI- and Anthropic-compatible endpoints
  and config-defined custom providers.
- Provider hooks for model metadata, account usage (`/usage`), Codex authentication, and
  DeepSeek's required reasoning replay.
- Built-in tools (`read`, `edit`, `write`, `bash`, `task_wait`, `web_fetch`, `web_search`, `skill`),
  background tasks, and `gax -p` subagents with recursion limits.
- A `skill` tool that loads a skill by name, so the skill listing in the system prompt carries
  descriptions without document paths.
- Live terminal rendering: incremental Markdown, streamed tool output, reflowed to the display
  width, with the Ctrl-T transcript view.
- Bash output compression (`bash.compress`, on by default): recognized shell commands (`ls`,
  `tree`, `cat`, `grep`/`rg`, `ast-grep`, `git status`/`diff`/`log`/`add`/`commit`/`push`,
  `cargo test`, `go test`, `pytest`, `npm test`/`jest`/`vitest`, `ruff`, `sqlfluff`, `docker ps`)
  reach the model as a compact form that says what it dropped, with the full output kept in a temp
  file the result names; `raw: true` on a `bash` call returns the output verbatim.
- Layered configuration (`GAX_*` environment variables, `config.json`, `state.json`,
  conversation selections), presets, model/effort pickers, and the models.dev catalog cache.
- Shell-like Tab completion of `/` command names and of the arguments `/provider`, `/model`,
  `/effort`, `/preset`, `/new`, and `/clear` enumerate, with a dim placeholder after a command
  name that takes one.
- CI (gofmt, `go vet`, race-enabled tests, cross-builds) and GoReleaser-based releases for Linux
  and macOS on amd64 and arm64.
