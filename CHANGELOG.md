# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-10-27

### Added

- Native Windows support across amd64 and arm64, including process-tree cleanup (Job Objects),
  console VT input/output, out-of-band file locking, and path normalization.
- Native Win32 clipboard support using direct User32/Kernel32 APIs (`CF_UNICODETEXT`, registered `PNG`,
  and `CF_DIB`/`CF_DIBV5` decoding), removing headless PowerShell subprocess latency while retaining
  it as a fallback.
- Transparent multi-shell execution on Windows: automatically adapt command line arguments for
  PowerShell (`pwsh`/`powershell` with `-NoProfile -NonInteractive -Command`), CMD (`/d /s /c`),
  and POSIX shells (`-c`).
- Automatic fallback on Windows to PowerShell or CMD when Git for Windows Bash is not installed,
  with support for explicit `bash.shell` configuration to PowerShell or CMD.
- Keenable search via `web.search_provider=keenable`, with `KEENABLE_API_KEY` (or
  `web.keenable_api_key`) for keyed access and a public endpoint when no key is configured.
- Windows CI coverage and release ZIPs alongside Linux/macOS tarballs and checksums.

### Fixed

- Prevent deadlock in full conversation history (`Ctrl+O`) and transcript (`Ctrl+T`) views when
  quitting the pager before all output has been consumed.
- Pass `-R` to fallback `less` invocations when `$PAGER` is unset, and recognize `less.exe` on Windows,
  so ANSI formatting renders styled text instead of raw escape sequences.
- Prevent 100% CPU busy-spin in `gax --resume` and slash-command pickers on macOS and BSD by waiting
  indefinitely in POSIX poll when no timeout is requested.
- Preserve multiline pastes on Windows by enabling VT input so the console passes
  bracketed-paste markers through instead of treating pasted newlines as Enter.
- Decode terminal Ctrl+Delete and the distinct CSI-u/modifyOtherKeys Ctrl+Backspace sequences to the
  editor's existing word-deletion actions.
- Map native Windows Ctrl+Backspace/Delete records to those actions and Shift+Enter to an inserted
  newline; unmodified Backspace/Delete and plain Enter behavior remain unchanged.
- Restore automatic palette colors in native Windows Terminal shells that set `WT_SESSION`
  without `TERM`, while respecting explicit terminal and color settings.
- Keep native Windows Terminal wheel/trackpad scrollback available during prompts, pickers,
  and running turns by disabling console mouse reporting.
- Prevent a race condition in the Linux parent-death test (`TestBashProcessDiesWhenItsParentIsKilled`)
  where reaped child processes and procfs state transitions falsely reported survival.

## [0.2.0-rc.4] - 2026-10-26

### Fixed

- Prevent deadlock in full conversation history (`Ctrl+O`) and transcript (`Ctrl+T`) views when
  quitting the pager before all output has been consumed.
- Pass `-R` to fallback `less` invocations when `$PAGER` is unset, and recognize `less.exe` on Windows,
  so ANSI formatting renders styled text instead of raw escape sequences.
- Prevent 100% CPU busy-spin in `gax --resume` and slash-command pickers on macOS and BSD by waiting
  indefinitely in POSIX poll when no timeout is requested.

## [0.2.0-rc.3] - 2026-10-26

### Added

- Keenable search via `web.search_provider=keenable`, with `KEENABLE_API_KEY` (or
  `web.keenable_api_key`) for keyed access and a public endpoint when no key is configured.
  Existing default search selection and DuckDuckGo fallback are unchanged.

### Fixed

- Preserve multiline pastes on Windows by enabling VT input so the console passes
  bracketed-paste markers through instead of treating pasted newlines as Enter.
- Decode terminal Ctrl+Delete and the distinct CSI-u/modifyOtherKeys Ctrl+Backspace sequences to the
  editor's existing word-deletion actions. Raw Backspace bytes remain ordinary Backspace where a
  terminal does not distinguish Ctrl+Backspace.
- Map native Windows Ctrl+Backspace/Delete records to those actions and Shift+Enter to an inserted
  newline; unmodified Backspace/Delete and plain Enter behavior remain unchanged.

## [0.2.0-rc.2] - 2026-10-23

### Fixed

- Restore automatic palette colors in native Windows Terminal shells that set `WT_SESSION`
  without `TERM`, while respecting explicit terminal and color settings.
- Keep native Windows Terminal wheel/trackpad scrollback available during prompts, pickers,
  and running turns by disabling console mouse reporting.

## [0.2.0-rc.1] - 2026-10-22

### Added

- Native Windows support on amd64 and arm64 using Git for Windows Bash, with Unicode console
  editing, process-tree cleanup, session/auth locking, Windows paths, and clipboard helpers.
- Windows CI coverage and release ZIPs alongside the Linux/macOS tarballs and checksums.

### Changed

- The build is cgo-free by contract: `make build`, `make install`, `make vet`, and the test targets
  pin `CGO_ENABLED=0`, so every local build matches the static release archives. Only `make test-race`
  enables cgo, because the race detector requires it.
- `gax --version` reports the `dev` default for a plain `go build` or `go install` instead of the
  upstream commit the port started from; release builds still inject the tag through `-ldflags`.
- gax no longer tracks a hax baseline. The README, AGENTS.md, and CONTRIBUTING.md describe gax as
  its own agent that was ported from hax, and the specs index is written against the current sources.

### Fixed

- Honor `context_limit` and `image_input` overrides for models absent from the catalog.
- Keep Go and embedded Markdown sources LF on Windows, and make palette and permission test
  fixtures independent of terminal defaults and Unix-only filesystem assumptions.

## [0.1.0] - 2026-10-12

Initial release: a minimalist terminal-native coding agent written in Go, ported from the C agent
[hax](https://github.com/OleksandrChekhovskyi/hax) and extended past it.

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
- `web_search` backends: You.com (the default, keyless through its free MCP profile), Brave, Octen,
  Exa, Firecrawl, and TinyFish, selected by `web.search_provider` or by the first configured key,
  with the keyless DuckDuckGo crawl as the fallback when a backend cannot answer.
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

### Fixed

- A resumed session is no longer swept: the pruning marker compared path strings, and an `*os.Root`
  names its entries relative to the root, so the comparison missed and the sweep unlinked the file
  it was asked to keep. It now compares file identity.
- A project reached through a symlink keeps one session bucket. macOS hands out
  `/var/folders/...` while `os.Getwd` reports `/private/var/...`, so a resumed session could look
  in a bucket the writer never used.
- The test suite passes on macOS: it had never run there, because `go vet` failed on a helper
  defined only in a Linux-tagged test file.

[Unreleased]: https://github.com/luank3/gax/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/luank3/gax/releases/tag/v0.2.0
[0.2.0-rc.4]: https://github.com/luank3/gax/releases/tag/v0.2.0-rc.4
[0.2.0-rc.3]: https://github.com/luank3/gax/releases/tag/v0.2.0-rc.3
[0.2.0-rc.2]: https://github.com/luank3/gax/releases/tag/v0.2.0-rc.2
[0.2.0-rc.1]: https://github.com/luank3/gax/releases/tag/v0.2.0-rc.1
[0.1.0]: https://github.com/luank3/gax/releases/tag/v0.1.0
