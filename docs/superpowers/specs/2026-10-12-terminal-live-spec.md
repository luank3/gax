# Renderer spec

## Scope

`internal/terminal/live.Renderer` renders one provider turn as terminal bytes. Inputs are
`conversation.Event` values (`Event`), assembled tool calls with `coretools.ToolPresentation`
(`ToolStart`, `ToolOutput`, `ToolResult`), and caller clocks (`Tick`, `PausePending`,
`SetStatusLabel`). Output is the `io.Writer` passed to `NewRenderer`, plus the first writer error,
latched and returned by every later method (`Err`). Non-goal: the renderer never writes or mutates
conversation history, model-facing tool text, or JSONL records.

## Invariants

### Stream text deltas immediately

- **Rule:** Write each text delta as it arrives, trimming leading carriage returns and newlines
  only before the first answer chunk.
- **Source:** `internal/terminal/live/render.go:291` (`Event`), trim at `:294`.
- **Assertion:** `TestRendererStreamsTextAndDropsInitialLineEndings` in
  `internal/terminal/live/render_test.go:41`.
- **Rejected:** Holding the answer until `EventDone`: `hello` must reach the writer first.

---

### Mark only a rendered attempt on retry

- **Rule:** On `EventRetry`, write `[unexpected end]` for the attempt that rendered content, drop
  its parser state, and write no marker when nothing rendered.
- **Source:** `internal/terminal/live/render.go:509` (`Retry`), marker at `:533`, reset at `:541`.
- **Assertion:** `TestRendererRetryMarksOnlyVisibleAbandonedAttempt` in
  `internal/terminal/live/render_test.go:148`, and `:173` for a retry that rendered nothing.
- **Rejected:** Reusing the stream across retries, which `:173` shows must add no marker.

---

### Latch the first writer error

- **Rule:** After the writer's first failure, return that same error from every later method.
- **Source:** `internal/terminal/live/render.go:1138` (`write`), `:1193` (`fail`).
- **Assertion:** `TestRendererLatchesWriterFailure` in `internal/terminal/live/render_test.go:873`.
- **Rejected:** Retrying the write: `writer.calls` must stay `1` after two `Event` calls.

---

### Keep one open row per collapsed cluster

- **Rule:** Append a `read` call to the cluster's open row as `, <argument>` while it fits in
  `width-1` cells, coalescing only `read`, and keep that row across `EventDone` and `Begin`.
- **Source:** `internal/terminal/live/render.go:644` (`startCollapsedTool`), fit at `:718`,
  survival at `:475` (`streamBoundary`).
- **Assertion:** `TestRendererCoalescesAdjacentCollapsedReadCalls` in
  `internal/terminal/live/render_test.go:521`, and
  `TestRendererKeepsCollapsedReadClusterAcrossProviderBoundaries` in `:547`.
- **Rejected:** Coalescing any tool or width: `:695` keeps `[bash]` alone, `:668` splits a cluster.

---

### Never repeat bytes the display sink already streamed

- **Rule:** Suppress a tool result's canonical output once the display sink wrote bytes, and
  render streamed bytes as plain output even under `OutputUnifiedDiff`.
- **Source:** `internal/terminal/live/render.go:794` (`ToolResult`), `:810` (gate), `:786` (style).
- **Assertion:** `TestRendererToolDisplayDoesNotDuplicateStreamedSummary` in
  `internal/terminal/live/render_test.go:722`, and `:778` for streamed file content.
- **Rejected:** Rendering `Result.Output` anyway: `:778` keeps a `--- ` body literal.

---

### Hold the newest preview row until it settles

- **Rule:** Emit only settled preview rows, holding the newest visible line until a successor or
  `Finalize`, opening with `┌` then `│` and closing with `\r└` (`\r›` for one row).
- **Source:** `internal/terminal/live/tool_render.go:74` (`Finalize`), `:433` (`emitRow`),
  `:470` (`closeLastRow`).
- **Assertion:** `TestToolPreviewStreamsSanitizedRowsAndClosesGutter` in
  `internal/terminal/live/tool_render_test.go:13`, and `:103` for the held newest row.
- **Rejected:** Emitting each line as it arrives: `Feed` must return `""` until `Finalize` runs.

---

### Paint status frames after the grace, never erasing first

- **Rule:** Paint the row only after `statusShowGrace`, one frame per write, erasing its tail
  after the paint and never the row before it.
- **Source:** `internal/terminal/live/render.go:852` (`Tick`), grace at `:885`, erase at `:930`.
- **Assertion:** `TestRendererShowsAndAnimatesDelayedWaitingSpinner` in
  `internal/terminal/live/render_test.go:235`, and `:307` for the no-pre-erase order.
- **Rejected:** Erasing with `\r\x1b[2K` first: `:307` fails on any `ansiEraseLine`.

---

### Do not paint status over an open block

- **Rule:** Paint no status frame while a streamed text, reasoning, or tool block is open.
- **Source:** `internal/terminal/live/render.go:880` (`Tick`).
- **Assertion:** `TestRendererDoesNotReviveWaitingStatusDuringTextOutput` in
  `internal/terminal/live/render_test.go:361`, and `:379` after a stream error.
- **Rejected:** Repainting every tick: `:379` ticks after an error and must see no row.

---

### Park status below an open collapsed row

- **Rule:** Park the indicator two rows below an open row with `\n\n`, and on clear climb back
  with `\x1b[2A` and restore the row's column with `\x1b[<cells+1>G`.
- **Source:** `internal/terminal/live/render.go:906` (`Tick`), restore at `:1064` (`clearStatus`).
- **Assertion:** `TestRendererParksStatusBelowOpenCollapsedReadRow` in
  `internal/terminal/live/render_test.go:580`, pinning `\r\x1b[2K\x1b[2A\x1b[13G, b.txt`.
- **Rejected:** Clearing at column zero, which would drop the next `, b.txt` off the open row.

---

## Gaps

- No test asserts any retry label or the countdown reset in `Event`
  (`internal/terminal/live/render.go:1084`, `:282`); no test file contains `retrying`.
- `EventProgress`'s `processing... %d%%` label (`internal/terminal/live/render.go:399`) has no test:
  no `_test.go` mentions it. Producer: `internal/provider/openai/chat_events.go:139`.
- `formatStatusDuration` (`internal/terminal/live/render.go:1120`) is pinned only for `30s` by
  `internal/terminal/live/render_test.go:262`; its minute and hour branches are unasserted.
- The `renderer.width <= 7` budget branch of `truncate`
  (`internal/terminal/live/tool_render.go:508`) is unexercised: preview tests use width 12 or
  wider (`internal/terminal/live/tool_render_test.go:45`).
- A visible reasoning delta sets `attemptRendered` (`internal/terminal/live/render.go:361`) while a
  hidden one returns first (`:337`), so a retry after reasoning-only output writes
  `[unexpected end]` untested.

## Review

- The parked-status state (`statusParkedRows`, `statusParkedColumn`, `collapsedLineOpen`) is
  written in `Tick` (`internal/terminal/live/render.go:906`) and read in `clearStatus` (`:1055`),
  `startCollapsedTool` (`:651`), and `closeCollapsedCluster` (`:1022`); one byte-exact test pins it.
- `statusLineNewlines` (`internal/terminal/live/render.go:905`, `:918`) exists only so `clearStatus`
  can restore `lineNewlines` (`:1072`) for `openBlock`'s separator count (`:1007`); the parked
  override to `0` (`:1074`) is where that bookkeeping most easily drifts.
- Unwrapped Markdown at `width <= 0` (`internal/terminal/live/render.go:83`) but a 100-cell tool
  layout (`:579`, `:681`, `internal/terminal/live/tool_render.go:57`); no test compares them.
- `ToolResult`'s dedup gate (`internal/terminal/live/render.go:810`) and the
  `execution.FallbackOutput != nil` branch (`:827`) have no test here; the producer side is
  `internal/tools/registry_test.go:436`.
