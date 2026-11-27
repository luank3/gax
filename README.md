# gax

Minimalist, terminal-native coding agent in Go (`CGO_ENABLED=0`), modeled on [hax](https://github.com/OleksandrChekhovskyi/hax).

## Features
- **Single static binary**: `CGO_ENABLED=0` across `linux`, `darwin`, and `windows` (`amd64`, `arm64`).
- **Providers**: `codex` (ChatGPT OAuth `/login`), `openai`, `anthropic`, `openrouter`, `opencode-zen`, `opencode-go`, `deepseek`, `llama.cpp` (port `9931` default), `ollama`, `openai-compatible`, `anthropic-compatible`, custom `providers.<id>`, and `mock`.
- **Tools**: `read` (text + images), `edit`, `write`, `bash` (auto-detach background tasks + output compression via `bash.compress`), `task_wait`, `skill` (`.agents/skills` + built-ins), `web_fetch`, `web_search` (8 backends + DuckDuckGo fallback), and lazy-loaded **MCP** (`mcp_tools` / `mcp_call` over stdio and streamable-HTTP).
- **Frontends**:
  - Interactive REPL (`gax`): native scrollback, streaming Markdown, live spinner, `Esc` pause / `Esc Esc` abort, `Ctrl+O` / `Ctrl+T` pager views, `Ctrl+R` history, `Ctrl+G` `$EDITOR`, `Ctrl+V` clipboard, `@` + `Tab` `fzf`, `OSC 7501` program status.
  - One-shot (`gax -p`, supports piped stdin + prompt) and JSONL stream (`gax --json`).
  - Remote control (`gax serve`): `127.0.0.1:8484` HTTP + SSE + POST server with embedded Preact + Tailwind CSS v4 web UI, constant-time bearer auth, and shared JSONL sessions.

## Build
```sh
make build        # builds web UI + ./bin/gax (-tags webui, CGO_ENABLED=0)
make test-race    # go test -race -count=1 ./...
make lint         # gofmt + go vet
make budget       # cross-builds 6 targets and verifies size/startup/RSS budgets
```

## Documentation
- [docs/v2/00-audit.md](docs/v2/00-audit.md) — Phase 0 parity audit and baselines
- [docs/v2/01-layout.md](docs/v2/01-layout.md) — package layering
- [docs/adr/](docs/adr/) — architecture decision records (`0001`–`0011`)
- [docs/dependencies.md](docs/dependencies.md) — approved modules and reasons
