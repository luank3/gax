# ADR-0004: Session Store Format

## Status
Accepted (Phase 1 — User confirmed Decision 1a)

## Context
`hax` stores conversations as append-only JSONL files under `$XDG_STATE_HOME/<app>/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl` (`docs/sessions.md`, `"version": 1`). The same record schema is emitted live by `--json`. `gax` v2 also adds `gax serve` (concurrent sessions, project registry, live web UI) and MCP tool calls.

## Options Considered
1. **Keep `hax` v1 JSONL format (additive fields only).**
   - *Pros*: 100% compatible with existing `gax` v1 / `hax` session files and `--json` scripts; append-only crash safety (`tail -f`, ignore unknown keys, skip partial final line, `/undo` appends a control record).
   - *Cons*: Concurrent `gax serve` writers must hold an in-process session mutex + file lock so two turns never interleave lines.
2. **Introduce a `version: 2` format or SQLite.**
   - *Pros*: Structured queries.
   - *Cons*: Breaks existing sessions and `--json` scripts; SQLite would require cgo or a large pure-Go dependency.

## Decision
Keep `hax` v1 JSONL format (`"version": 1`) as the single canonical store for both CLI and `gax serve`:
- **Location**: `$XDG_STATE_HOME/gax/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl` (with `history` alongside it per working directory).
- **Append-only + lazy reads**: Never rewrite existing lines; `/undo` appends `{"type":"undo","keep_user_turns":N}`; readers skip malformed final lines and ignore unknown keys.
- **Additive fields only**:
  - `tool_call` / `tool_result` work identically for MCP tools (e.g., `mcp__<server>__<tool>`).
  - Optional `source` field on `user` records (`"tui"`, `"oneshot"`, `"web"`) if helpful for diagnostics, ignored by older readers.
- **Concurrency (`gax serve`)**: A process-wide `session.Store` coordinates active sessions by `(cwd, session_id)` with an in-memory mutex and `platform.FileLock` so CLI and web runs cannot corrupt a file.

## Consequences
- A conversation started in the terminal can be resumed in `gax serve` and vice versa with zero migration.
- Memory stays bounded during long sessions because session files are streamed line-by-line on resume/listing rather than loaded into an in-memory database.
