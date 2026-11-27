# ADR-0007: Remote-Control Protocol (`gax serve`) and Threat Model

## Status
Accepted (Phase 1)

## Context
`gax serve` exposes a local HTTP server + Preact web UI to manage projects (registered working directories) and conversations (list, create, resume, fork, stream live output, interrupt) concurrently across projects, sharing the same JSONL session store as the CLI.

## Options Considered
1. **HTTP + Server-Sent Events (SSE) for streaming + `POST` for commands (stdlib `net/http`).**
   - *Pros*: Zero third-party Go modules; native browser `EventSource` / `fetch` streaming; trivial `Last-Event-ID` / sequence replay; works cleanly behind reverse proxies.
   - *Cons*: Unidirectional stream (client -> server uses `POST`), which fits a prompt/interrupt model well.
2. **WebSockets.**
   - *Rejected*: Go stdlib has no WebSocket package; `gorilla/websocket` or `coder/websocket` would add an external dependency with no functional need over SSE + POST.

## Decision

### 1. API Surface (`/api/v1/...`)
- `GET /api/v1/projects` — list registered project directories (and active session counts).
- `POST /api/v1/projects` — register a working directory (`{path}`).
- `GET /api/v1/projects/{id}/sessions` — list sessions in that project (from JSONL headers).
- `POST /api/v1/sessions` — create, resume (`resume_id`), or fork (`fork_from`, `keep_user_turns`) a session.
- `GET /api/v1/sessions/{id}/events` — SSE stream of `agent.Event`s (replays recent buffered events + live stream).
- `POST /api/v1/sessions/{id}/prompt` — submit user prompt (`{text}`).
- `POST /api/v1/sessions/{id}/interrupt` — pause (`mode: "pause"`) or immediate abort (`mode: "abort"`).

### 2. Threat Model & Security Controls
`gax serve` grants **remote code execution** (via `bash`, `edit`, `write`, MCP tools) to any authenticated caller:
- **Bind address**: Defaults to `127.0.0.1:8484` (loopback only). Binding to any non-loopback address requires an explicit `--listen` / `--allow-remote` flag and prints a prominent stderr warning.
- **Authentication**: Required bearer token generated at startup (`crypto/rand`, 32 bytes hex) or supplied via `GAX_SERVE_TOKEN` / `--token-file`. Verified via `crypto/subtle.ConstantTimeCompare` on `Authorization: Bearer <token>` (or an `HttpOnly; SameSite=Strict` session cookie exchanged once at `POST /api/v1/auth`). **Never** accept tokens in URL query parameters (`?token=...`), and **never** log tokens.
- **CSRF / Origin**: State-changing `POST`/`DELETE` endpoints reject requests whose `Origin` (when present) does not match the server's host, and require `Content-Type: application/json` (or `X-Gax-Auth` custom header) so simple HTML forms cannot trigger commands.
- **Filesystem boundary**: Project registration validates canonical paths; session IDs are validated as UUIDs (no path traversal).
- **Rate/Resource bounds**: HTTP request bodies capped (`1 MiB`), per-session SSE subscriber queue bounded (drop/disconnect slow consumers instead of unbounded memory growth).

## Consequences
- Zero third-party Go modules needed for `gax serve`.
- Safe by default on `127.0.0.1` with constant-time auth and Origin checks.
