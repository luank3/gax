# ADR-0005: MCP Lazy Loading & Client Architecture

## Status
Accepted (Phase 1)

## Context
`gax` v2 requires a built-in MCP client supporting stdio and streamable-HTTP transports, JSON-RPC 2.0, server lifecycle, timeouts, cancellation, crash recovery, and per-server config. Crucially:
- Tool definitions must be loaded **lazily**: the model sees a compact index of available servers/tools, and a full JSON schema enters context only when the task needs that tool.
- Nothing is spawned and nothing is allocated for configured but unused servers; with no servers configured, cost is zero.

## Options Considered
1. **Eagerly connect all configured MCP servers at startup and inject all tool schemas.**
   - *Rejected*: Violates the zero-cost-when-unused and lazy-loading constraints; bloats startup time, RSS, and prompt token count.
2. **Compact index + on-demand `mcp_discover` / `mcp_call` (or dynamic tool promotion) with disk-cached index.**
   - *Pros*: Zero processes spawned at startup; full schemas enter context only when requested; integrates cleanly with compaction.
   - *Cons*: Requires caching the tool index (name + one-line description) or lazy first-list on discovery.

## Decision

### 1. Configuration (`config.json`)
```json
{
  "mcp": {
    "servers": {
      "github": {
        "transport": "stdio",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": { "GITHUB_TOKEN": "$GITHUB_TOKEN" },
        "timeout": "30s"
      },
      "docs": {
        "transport": "http",
        "url": "https://mcp.example.com/mcp",
        "headers": { "Authorization": "Bearer $DOCS_TOKEN" },
        "timeout": "30s"
      }
    }
  }
}
```
If `mcp.servers` is empty, `mcp.Manager` is nil: zero goroutines, zero allocations, zero MCP tools advertised.

### 2. Lazy-Loading Mechanism
1. **Compact index in system prompt**: When `mcp.servers` is non-empty, the system prompt includes a compact `# MCP Servers` section listing each server name and (if cached in `$XDG_CACHE_HOME/gax/mcp/<server>.json`) its tool names + 1-line descriptions, without parameter schemas. If uncached, it lists only the server name.
2. **Two built-in MCP meta-tools** (only advertised when `mcp.servers` is non-empty):
   - `mcp_tools(server, tool?)`: Connects to `server` on demand (spawning stdio process or opening HTTP session), fetches `tools/list`, updates the disk cache, and returns either the compact tool list or the **full JSON schema** for `tool` (or all tools on `server` if `tool` is omitted and count is small).
   - `mcp_call(server, tool, arguments)`: Ensures `server` is connected (reusing the cached schema/session), validates arguments, invokes `tools/call` with `context.Context` timeout/cancellation, and bounds output via `tool_output_cap` (default 50 KiB).
3. **Context-budget accounting & compaction interaction**:
   - Because full schemas are returned as `tool_result` items of `mcp_tools`, their bytes/tokens are automatically counted in `agent` context-window estimation and compacted naturally like any other tool output when the conversation ages.
   - After compaction (or `/undo`), just like `skill` in `gax` v1 (`skillAlreadyInActiveContext`), if the model calls `mcp_tools` for a schema no longer in the active post-compaction window, it is re-emitted; if still in active context, a short `"already loaded above"` pointer is returned.
4. **Lifecycle & crash recovery**:
   - Stdio servers run in a `platform.ProcessGroup` and are closed when the session ends.
   - If a stdio server crashes mid-session, the next `mcp_call` or `mcp_tools` detects the closed pipe, respawns once with backoff, and re-runs `initialize`.

## Consequences
- Unused MCP servers cost 0 ms startup and 0 bytes RSS.
- Full schemas never bloat the system prompt across unrelated turns and age out via standard compaction.
- Client implementation choice (official SDK vs in-repo) is evaluated separately in ADR-0006.
