# Changelog

## [Unreleased] (v2)

### Added
- Clean 7-layer architecture enforced by `internal/archtest` (`internal/platform` isolates all `syscall`, `golang.org/x/sys`, and `runtime.GOOS` usage).
- Built-in lazy-loaded MCP client (`internal/mcp`, `mcp_tools`, `mcp_call`) over stdio and streamable-HTTP with disk index caching.
- `gax serve` remote-control HTTP + SSE + POST server with embedded Preact + Tailwind CSS v4 web UI (`--allow-remote`, constant-time bearer auth, Origin checks).
- `opencode-zen` provider, `program_status` (`OSC 7501`), focus-aware `notify` (`auto`/`bel`/`osc9`), piped stdin alongside `-p` prompt, and compaction overflow/`length` recovery.
- CI budget enforcement (`scripts/check-budgets.sh`).

### Changed
- `providers.llamacpp.port` default changed from `8080` to `9931`.
- Unconfigured `openai-compatible` / `anthropic-compatible` providers are hidden from `/provider` until `base_url` is set.
