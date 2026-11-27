# ADR-0001: Package Layering and Event Model

## Status
Accepted (Phase 1)

## Context
Current `gax` (`1aa430f`) has `internal/app` at 6,401 LOC mixing CLI parsing, REPL input, slash commands, pickers, signal handling, JSONL output, and OS-specific helpers. `internal/agent` communicates with frontends via ad-hoc callback fields on `LoopOptions` (`Observe`, `TurnBegin`, `ToolSeen`, `ToolCall`, `TaskNote`, `Flush`), and `internal/tools/registry.go` imports `syscall`. Adding `gax serve` (concurrent web sessions) and MCP on top of that coupling would duplicate loop/rendering logic across TUI, `-p`, `--json`, and HTTP/SSE.

## Options Considered
1. **Keep callback hooks on `agent.LoopOptions` and add a web adapter.**
   - *Pros*: Smaller initial diff if patching v1.
   - *Cons*: Every new frontend must re-wire 8+ callbacks; concurrent sessions in `gax serve` cannot cleanly multiplex or replay events to late-joining SSE clients; hard to enforce UI-agnostic core.
2. **Strict layered packages + typed event stream (`agent` -> `frontends`).**
   - *Pros*: Single source of truth for turn execution; TUI, `-p`, `--json`, and `gax serve` are pure consumers of `<-chan agent.Event` (or `EventSink`); dependency direction is mechanically enforced by a `go/parser` test.
   - *Cons*: Requires defining a clean, stable `Event` union/sum type up front.

## Decision
Adopt strict unidirectional package layering with a typed event stream emitted by `internal/agent`:

```text
Layer 0: internal/core       (pure types: Message, Item, Usage, ToolDef, Event, Error)
Layer 1: internal/platform   (ONLY package allowed to import syscall or check runtime.GOOS)
Layer 2: internal/config, internal/session, internal/catalog, internal/skill,
         internal/transport, internal/wiretrace, internal/bashcompress, internal/web
Layer 3: internal/provider/*, internal/tools, internal/mcp
Layer 4: internal/agent      (turn loop, compaction, stats, context window, event emitter)
Layer 5: internal/tui, internal/oneshot, internal/jsonstream, internal/serve
Layer 6: cmd/gax             (CLI entrypoint, wires frontends to agent)
```

### Rules
- **Dependency direction**: A package in Layer *N* may only import packages in Layers `< N` (and stdlib). Enforced by `internal/archtest/imports_test.go` using `go/parser`.
- **Consumer-defined interfaces**: Interfaces live only at real seams (`platform`, `provider.Client`, `tools.Tool`, `mcp.Transport`, `agent.EventSink`) and are defined by the consumer.
- **Typed `agent.Event` stream**:
  - `TurnStarted{Turn int, Provider, Model, Effort string}`
  - `TextDelta{Text string}` / `ReasoningDelta{Text string}`
  - `ToolCallSeen{CallID, Name string}` / `ToolCallStarted{CallID, Name, ArgsSummary string}`
  - `ToolOutputDelta{CallID, Stream, Chunk string}` / `ToolCallFinished{CallID, Name, Summary string, DurationMS int64, IsError bool}`
  - `TaskNote{Text string}`
  - `CompactStarted{Auto bool}` / `CompactFinished{BeforeTokens, AfterTokens int64, Err error}`
  - `TurnUsage{Usage core.Usage}`
  - `RetryScheduled{Attempt int, Delay time.Duration, Reason string}`
  - `TurnFinished{Outcome core.Outcome, Error string}`

## Consequences
- `internal/agent` has zero imports of `tui`, `serve`, `oneshot`, or `platform/tty`.
- `gax serve` can fan out `agent.Event` to multiple SSE subscribers per active session, while `tui` renders live regions and `--json` serializes session records from the same loop.
- No `utils`, `helpers`, or `common` catch-all packages are permitted.
