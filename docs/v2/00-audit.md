# gax v2 — Phase 0 Audit

## 1. Parity Matrix: hax (HEAD `059d3ab`) vs current gax (HEAD `1aa430f`)

Status legend:
- **present**: Implemented in current `gax` with matching behavior.
- **diverged**: Implemented in current `gax` with behavioral or default differences from `hax`.
- **missing**: Not present in current `gax`.

### 1.1 Providers & Auth

| Feature / Behavior | hax Reference | gax Status | Notes |
| --- | --- | --- | --- |
| `codex` (ChatGPT subscription, `/login` browser + device flow, token refresh, `~/.codex/auth.json` fallback) | `src/providers/codex*.c`, v0.4.0–v0.5.0 | **present** | `internal/provider/codex/` |
| `openai` (Responses API, `OPENAI_API_KEY`) | `src/providers/responses_*.c` | **present** | `internal/provider/openai/` |
| `anthropic` (Messages API, prompt caching 1h/5m, adaptive/budget thinking) | `src/providers/anthropic_*.c` | **present** | `internal/provider/anthropic/` |
| `openrouter` (`OPENROUTER_API_KEY`, `x-session-id`, `/usage` credits) | `src/providers/openrouter.c` | **present** | `internal/provider/openrouter/` |
| `opencode-zen` (`OPENCODE_API_KEY`, `x-opencode-session`, `x-opencode-client`) | `src/providers/opencode.c`, v0.4.0, v0.5.0 | **missing** | `gax` only registers `opencode-go` |
| `opencode-go` (`OPENCODE_API_KEY`, `/usage` limits) | `src/providers/opencode.c`, v0.4.0, v0.5.0 | **present** | `internal/provider/opencodego/` |
| `deepseek` (`DEEPSEEK_API_KEY`, `/usage` balance, reasoning replay) | `src/providers/deepseek.c`, v0.6.0 | **present** | `internal/provider/deepseek/` |
| `llama.cpp` (auto-discovery, router mode, `/props` context/image, port default) | `src/providers/llamacpp.c`, v0.4.0, v0.7.0 | **diverged** | `gax` defaults port to `8080`; `hax` v0.7.0 changed default to `9931` |
| `ollama` (`http://127.0.0.1:11434/v1`, explicit model) | `src/providers/registry.c` | **present** | `internal/provider/registry/` |
| `openai-compatible` / `anthropic-compatible` (one-off env endpoints) | `src/providers/registry.c`, v0.7.0 | **diverged** | `hax` v0.7.0 hides them from `/provider` & completion until `base_url` is set |
| Custom `providers.<id>` (`api`, `model_apis`, `catalog_id`, `metadata_api`, `extra_body`, `extra_headers` with `{session_id}`, `overflow_phrases`) | `src/providers/provider_config.c`, v0.4.0–v0.6.0 | **present** | `internal/provider/config/`, `internal/provider/runtime/` |
| `reasoning_required` (`auto` via catalog / explicit) | `src/providers/chat_body.c`, v0.6.0 | **missing** | Absent from `gax` settings & Chat Completions body builder |
| `reasoning_roundtrip` (`auto`, `off`, or field name) | `src/providers/chat_*.c`, v0.6.0 | **present** | `internal/provider/openai/` |
| OpenAI/Codex optional tool params not forced required (v0.6.1 fix) | `src/tool_schema.c`, v0.6.1 | **diverged** | Verify schema `required` array in `gax` v2 port |
| `mock` provider (`GAX_MOCK_SCRIPT`, heuristic fallback) | `src/providers/mock.c` | **present** | `internal/provider/mock/` |
| models.dev catalog cache (`catalog.url`, `catalog.refresh`, `catalog.models` overrides) | `src/catalog.c`, `src/model_meta.c` | **present** | `internal/catalog/` |
| Version-aware `/model` sorting (`sort_models`) | `src/model_sort.c`, v0.4.0, v0.6.0 | **present** | `internal/provider/modelsort/` |

### 1.2 CLI, One-Shot Mode & Sessions

| Feature / Behavior | hax Reference | gax Status | Notes |
| --- | --- | --- | --- |
| Interactive REPL, `-p`/`--print`, `--json`, `-c`/`--continue`, `--resume[=ID]`, `--no-session`, `--raw`, `--bare` | `src/cli.c`, `src/oneshot.c` | **present** | `internal/cli/`, `internal/app/` |
| Preset shorthand (`hax <preset>`) | `src/cli.c`, v0.6.0 | **present** | `internal/cli/` |
| Piped stdin alongside `-p "<prompt>"` (`gh pr diff \| hax -p "review"`) | `src/cli.c`, `[Unreleased]` (`64ddf49`) | **missing** | `gax` `cli.ReadPrompt` ignores stdin when positional args exist |
| One-shot `max_turns` default `0` (unlimited) | `src/config.c`, v0.6.0 | **present** | `internal/config/settings.go` |
| Clean one-shot signals (`SIGINT`/`SIGTERM` -> 130, `SIGUSR1` -> pause at turn boundary) | `src/oneshot.c`, v0.5.0 | **diverged** | Unix has `SIGUSR1`; Windows lacks `SIGUSR1` |
| Append-only JSONL sessions (`version: 1`, `selection`, `undo`, `user_turn`, `turn_boundary`, `turn_usage`, `inherited`) | `src/session.c`, `docs/sessions.md`, v0.6.0 | **present** | `internal/session/` |
| Per-cwd prompt history (`<sessions-dir>/history`) | `src/history.c`, v0.6.0 | **present** | `internal/session/` |
| Session pruning (`session_retention_days`, symlink-safe) | `src/session_prune.c` | **present** | `internal/session/` |

### 1.3 Agent Loop, Compaction & Tools

| Feature / Behavior | hax Reference | gax Status | Notes |
| --- | --- | --- | --- |
| Turn loop, streaming events, pause (1st Esc) & immediate interrupt (2nd Esc) | `src/agent_loop.c`, `src/turn.c` | **present** | `internal/agent/` |
| Compaction (`/compact [focus]`, automatic at `compact.threshold`) | `src/compact.c` | **present** | `internal/agent/compact.go` |
| Compaction recovery on context-overflow errors & oversized windows (shortened copy, suspend repeated failures) | `src/compact.c`, v0.7.0 (`ae47988`, `5f81452`, `db9b771`) | **missing** | `gax` fails on context overflow instead of compacting & retrying |
| Compaction recovery when reply fills known context window (`response incomplete: length`) | `src/agent_loop.c`, `[Unreleased]` (`aca988f`) | **missing** | Added in `hax` after v0.7.0 |
| Esc lets active compaction finish before pausing; returns unsent message to prompt | `src/agent.c`, `src/compact.c`, v0.7.0 (`ada7007`) | **missing** | — |
| Core tools: `read` (text + images), `edit`, `write`, `bash` (auto-detach on timeout), `task_wait` (with `kill`) | `src/tools/*.c` | **present** | `internal/tools/` |
| Background task lifecycle & `/tasks` | `src/tools/task_registry.c`, `src/commands/tasks.c` | **present** | `internal/tools/` |
| Prompt guidance favoring separate tool calls over chained shell commands | `src/agent_core.c`, `[Unreleased]` (`38c716c`) | **missing** | — |
| Subagent guidance always explains `--preset <name>` | `src/agent_env.c`, v0.7.0 (`bfb107f`) | **missing** | — |
| `AGENTS.md` hierarchy & `.agents/skills` discovery | `src/agent_env.c`, v0.5.0 | **present** | `internal/skill/`, `internal/agent/` |

### 1.4 Terminal UI, Rendering & Notifications

| Feature / Behavior | hax Reference | gax Status | Notes |
| --- | --- | --- | --- |
| Native scrollback preserved, live-region-only redraws | `src/terminal/live_region.c`, `src/render/disp.c` | **present** | `internal/terminal/live/` |
| Persistent live-updating status/spinner/elapsed/footer below streaming text & tools; status label hysteresis | `src/render/spinner.c`, `src/render/footer.c`, v0.7.0 (`c7a119d`, `ddcce33`, `45c7e61`, `2bd5918`) | **missing** | `gax` does not stack persistent spinner/status under streaming output |
| Streaming Markdown reflow, tables, diff coloring | `src/render/markdown*.c`, `src/render/diff_color.c` | **present** | `internal/terminal/markdown/` |
| Readline-style editor, `@file` fzf picker, Ctrl-V image/text paste, Ctrl-G `$EDITOR`, Ctrl-O history pager, Ctrl-T transcript pager | `src/terminal/input*.c`, `src/paste_image.c`, `src/file_mention.c` | **present** | `internal/terminal/*`, `internal/app/` |
| Prompt polish: delayed slash-command hints, trim surrounding whitespace, erase empty prompt on quit | `src/terminal/input.c`, v0.7.0 (`08ef33f`, `ef5e597`, `a852ac0`) | **missing** | — |
| Slash-command Tab completion & pickers (Ctrl-J/K, Ctrl-W) | `src/slash.c`, `src/terminal/picker*.c`, v0.6.0 | **present** | `internal/slash/`, `internal/terminal/picker/` |
| Program status reporting (`OSC 7501`, `program_status`) | `src/terminal/notify.c`, `[Unreleased]` (`fc7a1b9`) | **missing** | — |
| Focus-aware `notify` (`auto` rings bell only when unfocused, `bel` always, `osc9` with answer preview) | `src/terminal/focus.c`, `src/terminal/notify.c`, `[Unreleased]` (`fc7a1b9`) | **diverged** | `gax` has `notify` without focus tracking or `osc9` answer preview |

### 1.5 gax-only Features (In Scope for v2)

| Feature | Current gax Location | Notes |
| --- | --- | --- |
| `web_fetch` (HTML-to-text, byte/time limits, binary detection) | `internal/web/fetch.go`, `internal/web/html.go`, `internal/tools/web.go` | Port & refine |
| `web_search` (You.com, Brave, Octen, Exa, Firecrawl, TinyFish, Keenable, DuckDuckGo fallback) | `internal/web/search.go` | Port; factor repeated JSON POST backends |
| Bash output compression (`bash.compress`, `raw: true`, temp-file spill) | `internal/bashcompress/` | Port as-is |
| `skill` tool + built-in skills (`code-intelligence`, `customize-gax`) + active-context reuse guard | `internal/tools/skill.go`, `internal/skill/` | Port as-is |
| Subagent depth guard (`GAX_SUBAGENT_DEPTH`, `MaxDepth = 3`) | `internal/subagent/depth.go` | Port as-is |
| Native Windows support (Job Objects, Win32 console VT, Win32 clipboard, PowerShell/CMD/Git Bash) | `internal/system/console/`, `internal/tools/bash_process_windows.go`, `internal/terminal/paste/clipboard_windows.go` | Redesign behind `platform` interface |

---

## 2. Review of Current gax: Port vs Redesign vs Drop

### Worth porting (with cleanup)
- **`internal/bashcompress/`** (3,424 LOC + 1,129 test LOC): Self-contained, zero OS coupling, well-tested command filters and `never_worse` invariant.
- **`internal/web/`** (1,735 LOC + 1,405 test LOC): `fetch.go` and `html.go` are solid. In `search.go`, the `settledError` fallback logic is sound (avoids masking config/auth errors or empty results), but the 5 JSON-POST backends (`octen`, `exa`, `firecrawl`, `tinyfish`, `keenable`) duplicate HTTP/status/error plumbing and should share a small helper.
- **`internal/skill/` & `internal/tools/skill.go`** (569 + 139 LOC): Clean skill discovery, embedded built-in skills, and active-context deduplication.
- **`internal/subagent/depth.go`** (56 LOC): Simple, fail-closed recursion guard.
- **`internal/transport/` & `internal/wiretrace/`** (1,207 + 249 LOC): Streaming HTTP/SSE, retry/backoff, secret redaction, and `ca_roots.go` (move `runtime.GOOS` check in `ca_roots.go` to `platform` or build tags).
- **`internal/catalog/`** (908 LOC), **`internal/session/`** (1,551 LOC), **`internal/config/`** (1,328 LOC): Good functional coverage; in `session/path.go`, remove `runtime.GOOS` checks via `platform`.
- **Pure terminal engines** in `internal/terminal/`: `markdown` (2,721 LOC), `width` (507 LOC), `vtresolve` (575 LOC), `theme` (271 LOC), `editbuffer` (112 LOC).

### Redesign
- **`internal/app/` (6,401 LOC)**: Monolithic package mixing CLI dispatch, REPL loop, one-shot/JSON output, slash command execution, pickers, pager, signals, and OS helpers (`store_path_*.go`, `signals*.go`). **Redesign**: agent core emits a typed event stream; `tui`, `oneshot`, `jsonstream`, and `serve` become independent frontend consumers.
- **`internal/system/*` + scattered OS code**: Currently 40+ non-test `runtime.GOOS` / `syscall` references leak across `app`, `provider/codex`, `session`, `tools`, `terminal/*`, and `transport`. **Redesign**: a single `platform` layer behind build tags with one interface per concern (terminal raw mode/size, shell resolution, process-tree kill, clipboard, config/state/cache dirs, file locking, keep-awake, browser open). Enforce via an import-direction & forbidden-import test.
- **`internal/tools/` (5,209 LOC)**: `registry.go` imports `syscall` and mixes tool definitions with OS process management. **Redesign**: tools depend only on core types and `platform` interfaces.
- **`internal/provider/runtime/` (2,618 LOC)**: Oversized hub with hard-coded hook switches. **Redesign**: consumer-defined small interfaces (`Provider`, `ModelLister`, `UsageQuerier`, `Authenticator`).

### Drop
- **`docs/superpowers/`**: v1 porting plans and specs; superseded by `docs/v2/` and `docs/adr/`.

---

## 3. Baseline Measurements of Current gax

Measured on `linux/arm64` (Go `go1.27.3`), built with `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`:

### Binary sizes (all 6 targets)

| Target | Size (bytes) | Size (MiB) |
| --- | ---: | ---: |
| `linux/amd64` | 10,203,296 | 9.73 MiB |
| `linux/arm64` | 9,371,808 | 8.94 MiB |
| `darwin/amd64` | 10,406,480 | 9.92 MiB |
| `darwin/arm64` | 9,575,138 | 9.13 MiB |
| `windows/amd64` | 10,664,960 | 10.17 MiB |
| `windows/arm64` | 9,627,648 | 9.18 MiB |

Top symbol-size contributors in unstripped `linux/arm64` (`go tool nm -size`): `crypto/internal/fips140/*` (notably `drbg`, `nistec`, `edwards25519`, `mldsa`), `github.com/luank3/gax/*` (~1.16 MB), `runtime` (~765 KB), `crypto/tls` (~224 KB), `net/http` (~206 KB), `net/http/internal/http2` (~189 KB), `encoding/json/v2` (~138 KB), `net` (~131 KB).

### Runtime memory & startup (`linux/arm64`, mock provider)

| Measurement | Result |
| --- | --- |
| Startup time (`gax --version`, 10 runs) | min `10.02 ms`, median `11.00 ms`, max `23.94 ms` |
| One-shot mock (`GAX_PROVIDER=mock gax -p "hello"`) | elapsed `120.30 ms`, max RSS `13,916 KiB` (`13.59 MiB`) |
| Idle REPL (PTY, `GAX_PROVIDER=mock`, settled 500 ms) | `VmRSS: 13,512 KiB` (`13.19 MiB`) |
| Long session (100 mock turns w/ `bash` tool calls, `-p`) | elapsed `103.95 ms`, max RSS `12,788 KiB` (`12.49 MiB`) |

---

## 4. Proposed Budgets (Enforced in CI)

| Metric | Baseline (current gax) | Proposed v2 Budget | Notes |
| --- | --- | --- | --- |
| Binary size (`amd64` targets) | 9.73–10.17 MiB | **<= 12.0 MiB** | Includes embedded web UI assets + MCP + `serve` |
| Binary size (`arm64` targets) | 8.94–9.18 MiB | **<= 11.0 MiB** | Includes embedded web UI assets + MCP + `serve` |
| Embedded web UI assets (gzipped) | N/A | **<= 60 KiB** | Preact + purged CSS + app bundle |
| Startup time (`gax --version` median) | 11.00 ms | **<= 15 ms** | Measured on Linux CI runner |
| Idle RSS (REPL / idle `serve`) | 13.19 MiB | **<= 18 MiB** | No MCP servers spawned until used |
| Peak RSS (defined workload) | 13.59 MiB (CLI) | **<= 32 MiB** | Workload: 100-turn mock session w/ 50 KiB tool outputs + `serve` streaming 1 SSE client |

---

## 5. Expected Dependencies

### Go

| Need | Recommendation | Reason |
| --- | --- | --- |
| HTTP client/server, SSE, TLS, JSON, CLI, crypto | **stdlib** | `net/http`, `encoding/json`, `crypto/*` cover all needs; zero external modules |
| Platform layer (Windows console, Job Objects, POSIX termios/ioctl/flock) | **in-repo** (`syscall` / `LazyDLL`) vs `golang.org/x/sys` | Current `gax` already works with stdlib `syscall` + `LazyDLL`; evaluate `golang.org/x/sys` in Phase 1 ADR with binary-size delta |
| MCP client (stdio + streamable-HTTP JSON-RPC 2.0) | **in-repo** vs official `modelcontextprotocol/go-sdk` | Hand-written JSON-RPC 2.0 client is ~400 LOC with zero transitive deps; evaluate SDK vs in-repo in Phase 1 ADR with binary-size & transitive-dep audit |
| `gax serve` transport (HTTP + SSE + POST) | **stdlib** | SSE + POST requires no WebSocket library |

### JS (build-time only, `web/` for `gax serve`)

| Need | Recommendation | Reason |
| --- | --- | --- |
| UI library | **third-party**: `preact` | Required by spec (~4 KiB gzipped) |
| Bundler | **third-party**: `vite` + `@preact/preset-vite` | Build-time only; outputs static assets for `embed` |
| CSS framework | **third-party**: Tailwind CSS v4 (`tailwindcss` + `@tailwindcss/vite`) vs UnoCSS | Measure both in Phase 1 prototype ADR; select smallest purged output |

---

## 6. Open Questions & Decisions Needed

See [decisions-needed.md](./decisions-needed.md) for the full list with recommendations. Summary:
1. **Session store compatibility**: Keep 100% `hax` v1 JSONL compatibility (with optional additive fields for MCP/web) vs a v2 format. **Recommendation**: Keep `hax` v1 JSONL compatibility so existing `hax`/`gax` sessions and `--json` scripts work seamlessly.
2. **Windows terminal & process control**: Stdlib `syscall` + `LazyDLL` (as in current `gax`) vs `golang.org/x/sys/windows`. **Recommendation**: Keep stdlib `syscall` + `LazyDLL` isolated in `platform/` (zero third-party deps), pending Phase 1 ADR measurements.
3. **MCP client implementation**: In-repo minimal JSON-RPC 2.0 client vs `github.com/modelcontextprotocol/go-sdk`. **Recommendation**: In-repo client (zero deps, smaller binary, full control over lazy schema loading), pending Phase 1 ADR comparison.
4. **Committed web build assets**: Commit built `internal/web/ui/dist/` vs build on release. **Recommendation**: Commit built assets (required for `go install` without Node), verified in CI by `make web` + `git diff --exit-code`.
5. **Dual `web_search` provider selection**: Keep `gax`'s 8-backend table (`you` default keyless, then key-based, `duckduckgo` last resort) or simplify. **Recommendation**: Keep all 8 backends, refactoring the 5 JSON-POST backends into a single table-driven helper.
