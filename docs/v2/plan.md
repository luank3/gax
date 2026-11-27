# gax v2 — Phase 2 Implementation Plan

Each slice is independently buildable, testable, and committable. No slice starts until the previous slice passes `gofmt`, `go vet`, `go test -race`, and 6-target `CGO_ENABLED=0` cross-compilation.

Updated budgets (per Phase 1 Gate):
- Binary size: `<= 14.5 MiB` (`amd64`), `<= 13.5 MiB` (`arm64`), embedded UI `<= 60 KiB` gzipped
- Idle RSS: `<= 18 MiB`; Peak RSS (100-turn + `serve` SSE): `<= 32 MiB`; Startup: `<= 15 ms`

---

## Slice 1: Repo Scaffold, Core Types, Platform Layer & Architecture Gate
- **Packages**: `go.mod`, `Makefile`, `internal/core`, `internal/platform`, `internal/archtest`, `cmd/gax` (minimal `--version` / `--help` stub).
- **Scope**:
  - Pin `golang.org/x/sys v0.50.0` in `go.mod`.
  - Define Layer 0 types (`Message`, `Item`, `Usage`, `ToolDef`, `Event`, `Outcome`, `SessionHeader`).
  - Implement `internal/platform` behind build tags: `Terminal`, `Shell`, `ProcessGroup` (POSIX `setpgid`/`kill(-pgid)` + `Pdeathsig` on Linux; Windows Job Objects + `CREATE_SUSPENDED` + `NtResumeProcess`), `Clipboard`, `Dirs` (XDG + symlink-canonical project bucket), `FileLock` (`flock` / `LockFileEx`), `KeepAwake`, `OpenBrowser`.
  - `internal/archtest/imports_test.go`: AST import-direction check + zero `syscall` / `golang.org/x/sys` / `runtime.GOOS` outside `internal/platform`.
- **Acceptance Criteria**:
  - `go test -race ./...` passes, including `archtest` import checks and `platform` tests (tempdir locking, process-group kill, shell resolution, XDG dirs).
  - Cross-compiles cleanly (`CGO_ENABLED=0`) for all 6 targets.

## Slice 2: Config, Catalog, Transport & Wire Trace
- **Packages**: `internal/config`, `internal/catalog`, `internal/transport`, `internal/wiretrace`.
- **Scope**:
  - Settings registry with layered resolution (`CLI override -> resumed session -> GAX_* env -> state.json -> config.json -> default`), presets, `providers.<id>` blocks, `program_status`, `reasoning_required`, `llamacpp.port` default `9931`.
  - `catalog` cache (`models.dev`, 24h refresh, `catalog.models` overrides).
  - `transport` streaming HTTP/SSE client, retry/backoff, idle timeout, OS CA roots, `wiretrace` secret redaction.
- **Acceptance Criteria**:
  - Table-driven unit tests for config precedence, duration/byte/token-count parsing, SSE framing, retry backoff, and redaction of `$VAR` headers/API keys in wire traces.

## Slice 3: Providers & Mock Provider
- **Packages**: `internal/provider`, `internal/provider/{openai,anthropic,codex,openrouter,opencode,deepseek,llamacpp,mock,registry}`.
- **Scope**:
  - Consumer-defined `provider.Client`, `ModelLister`, `UsageQuerier`, `Authenticator` interfaces.
  - OpenAI Responses + Chat Completions (`reasoning_format`, `reasoning_roundtrip`, `reasoning_required`, optional tool params not forced required), Anthropic Messages (prompt caching, adaptive/budget thinking), Codex OAuth (browser + device + `~/.codex/auth.json` fallback), OpenRouter, OpenCode Zen + OpenCode Go (`x-opencode-session`), DeepSeek, llama.cpp (port `9931`, router mode, `/props`), Ollama, custom providers (`model_apis`, `extra_body`, `extra_headers` with `{session_id}`).
  - Hide `openai-compatible` / `anthropic-compatible` from `/provider` until `base_url` is set.
  - Scripted `mock` provider supporting `hax`'s directive format (`text`, `reasoning`, `space`, `tool`, `delay`, `usage`, `overflow`, `end-turn`, `{{CWD}}`).
- **Acceptance Criteria**:
  - `httptest.Server` fixtures test request bodies, SSE event parsing, error mapping, and `overflow` detection for every wire dialect.
  - `mock` provider passes ported `hax` mock script tests.

## Slice 4: Core Tools & Skill System
- **Packages**: `internal/skill`, `internal/subagent`, `internal/tools`.
- **Scope**:
  - `read` (line offset/limit, image sniffing + size/dim caps), `edit`, `write` (unified diff), `bash` (platform `Shell` + `ProcessGroup`, timeout -> background task detach, `raw` flag), `task_wait` (with `kill` + `timeout_seconds`), `skill` tool + `.agents/skills` discovery + built-in `code-intelligence` & `customize-gax` skills + active-context reuse guard, `subagent` depth guard (`GAX_SUBAGENT_DEPTH`, `MaxDepth = 3`).
- **Acceptance Criteria**:
  - Unit tests for `read`/`edit`/`write`/`bash`/`task_wait`/`skill` including background detach, process-tree kill, image cap refusal, and skill deduplication.

## Slice 5: Agent Turn Loop, Compaction & Overflow Recovery
- **Packages**: `internal/agent`, `internal/transcript`.
- **Scope**:
  - Pure turn loop emitting typed `agent.Event`s; 1st pause vs 2nd immediate abort; background task note injection; system prompt assembly (`AGENTS.md` hierarchy, Environment, Skills, Subagent `--preset` guidance, separate-tool-call guidance).
  - Compaction (`/compact [focus]`, auto at `compact.threshold` / 16k headroom): recovers from context-overflow rejections (`overflow_phrases`) and `response incomplete: length`, summarizes shortened copy if full window overflows, suspends repeated failures, and lets Esc finish active compaction or restore unsent prompt.
  - `transcript` model-facing text renderer (`Ctrl+T` / `GAX_TRANSCRIPT`).
- **Acceptance Criteria**:
  - In-memory tests using `mock` provider verify: multi-turn tool loop, pause/abort invariants (unfinished reasoning discarded), context-overflow recovery, `length` recovery, and compaction suspension on repeated failure.

## Slice 6: Sessions, History & One-Shot / JSON Frontends
- **Packages**: `internal/session`, `internal/oneshot`, `internal/jsonstream`, `cmd/gax`.
- **Scope**:
  - Append-only `hax` v1 JSONL session writer/reader (`selection`, `undo`, `user_turn`, `turn_boundary`, `turn_usage`, `inherited`), per-cwd `history`, retention pruning (symlink-safe).
  - `oneshot` (`-p`): piped stdin + positional prompt (`gh pr diff | gax -p "review"`), clean stdout, stderr banner/stats/resume hint, `SIGINT`/`SIGTERM` (130) & `SIGUSR1` pause.
  - `jsonstream` (`--json`): live JSONL records + closing `result` record.
- **Acceptance Criteria**:
  - Round-trip session tests (write, crash-truncated tail, `/undo`, `/fork`, resume selection restore).
  - E2E tests of built binary for `-p`, piped stdin + prompt, `--json`, `--resume`, and signal handling.

## Slice 7: Interactive TUI Frontend
- **Packages**: `internal/tui` (subpackages `markdown`, `editor`, `live`, `picker`, `theme`, `width`, `vtresolve`, `notify`, `slash`).
- **Scope**:
  - Native scrollback preservation, persistent live-updating spinner/status/footer below streaming output with label hysteresis, streaming Markdown reflow + diff coloring, readline editor + delayed slash hints + whitespace trim + erase empty prompt on quit, Tab completion, `@file` fzf picker, `Ctrl+V` image/text paste, `Ctrl+G` `$EDITOR`, `Ctrl+O`/`Ctrl+T` `$PAGER`, `OSC 7501` `program_status`, focus-aware `notify` (`auto` / `bel` / `osc9` preview), all slash commands (`/new`, `/resume`, `/fork`, `/undo`, `/provider`, `/model`, `/effort`, `/preset`, `/preset-save`, `/config`, `/compact`, `/copy`, `/tasks`, `/session`, `/usage`, `/login`, `/logout`, `/help`).
- **Acceptance Criteria**:
  - Unit tests for Markdown, width, VT resolve, editor key sequences, slash completion, and notification formatting.
  - PTY/tmux smoke tests against `mock` provider covering streaming, Esc pause/abort, `/model` picker, and resize.

## Slice 8: Web Tools & Bash Output Compression
- **Packages**: `internal/web`, `internal/bashcompress`, wired into `internal/tools` and `internal/config`.
- **Scope**:
  - `web_fetch` (HTML-to-text, `strip: false`, byte/time limits) and `web_search` (all 8 backends: `you`, `brave`, `octen`, `exa`, `firecrawl`, `tinyfish`, `keenable`, `duckduckgo` fallback, with shared JSON-POST helper and `settledError` handling).
  - `bashcompress` command parser + filters (`ls`, `tree`, `cat`, `grep`/`rg`, `ast-grep`, `git`, `cargo`, `go`, `pytest`, `npm`/`jest`/`vitest`, `ruff`, `sqlfluff`, `docker`) with `never_worse` invariant and temp-file spill (`raw: true` bypass).
- **Acceptance Criteria**:
  - `httptest` coverage for all 8 search backends + fallback behavior + HTML-to-text conversion.
  - Fixture tests for every `bashcompress` filter + `never_worse` property check.

## Slice 9: MCP Client & Lazy Tool Discovery
- **Packages**: `internal/mcp` (pinned `github.com/modelcontextprotocol/go-sdk v1.8.0`), wired into `internal/tools` and `internal/agent`.
- **Scope**:
  - Stdio and streamable-HTTP transports, per-server config (`mcp.servers.*`), timeouts, cancellation, crash recovery (respawn once).
  - Zero allocations/processes when unused; disk index cache in `$XDG_CACHE_HOME/gax/mcp/`; compact `# MCP Servers` prompt index; `mcp_tools` and `mcp_call` tools with compaction-aware deduplication and `tool_output_cap`.
- **Acceptance Criteria**:
  - Tests with a fake stdio MCP server binary & `httptest` streamable-HTTP server verifying: zero spawn when unused, lazy connect on `mcp_tools`, schema caching, crash recovery, and post-compaction re-emit.

## Slice 10: `gax serve` & Preact Web UI
- **Packages**: `internal/serve`, `internal/serve/ui`, `web/`.
- **Scope**:
  - HTTP + SSE + POST `/api/v1/...` server, project registry, concurrent session manager sharing `internal/session`, `127.0.0.1` default, `--allow-remote` warning, constant-time bearer auth, `HttpOnly; SameSite=Strict` cookie exchange, Origin/CSRF checks, bounded SSE queues.
  - Preact + Tailwind CSS v4 SPA (`web/`): design tokens, keyboard-first shortcuts, mobile drawer, project/session switcher, create/resume/fork/interrupt, live streaming view.
  - `embed_webui.go` (`//go:build webui`) + `embed_stub.go` (`//go:build !webui`).
- **Acceptance Criteria**:
  - `httptest` tests for auth (constant-time, no URL tokens), Origin rejection, concurrent session SSE streaming, and CLI/web session sharing.
  - `make web` succeeds and total gzipped `internal/serve/ui/dist/` is `<= 60 KiB`.

## Slice 11: Release Pipeline, CI & Budget Verification
- **Files**: `.github/workflows/ci.yml`, `.goreleaser.yaml`, `Makefile`, `scripts/check-budgets.sh`.
- **Scope**:
  - CI runs: `gofmt`, `go vet`, `go test -race`, `make web`, 6-target `CGO_ENABLED=0` build, budget gate (binary size `<= 14.5 MiB` amd64 / `<= 13.5 MiB` arm64, web assets `<= 60 KiB` gz, startup `<= 15 ms`, peak RSS `<= 32 MiB`).
  - GoReleaser builds static binaries/archives for all 6 targets with `make web` hook.
- **Acceptance Criteria**:
  - Full CI pipeline script passes locally on Linux (with Windows/macOS runtime behavior noted as CI-runner verified).
