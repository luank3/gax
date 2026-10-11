# Live Terminal Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port Hax's live interactive TTY rendering for streamed assistant output and built-in tool calls while leaving canonical conversation history unchanged.

**Architecture:** Port the upstream incremental Markdown engine into Go and make both static and live Markdown use it. Add a stateful terminal renderer that consumes provider events, per-call tool presentation metadata, and a display sink in the tool registry; wire both paths only into the TTY REPL. One-shot, JSONL, and non-TTY output keep their current contracts.

**Tech Stack:** Go standard library `io`, existing `internal/terminal/markdown`, `internal/terminal/width`, `internal/agent` hooks, and built-in `internal/tools`.

**Spec:** `docs/superpowers/specs/2026-10-10-live-terminal-rendering-design.md`

## Global Constraints

- Stream presentation never mutates provider events, session items, tool-result text, JSONL records, or persisted history.
- TTY rendering is enabled only for the interactive REPL with a TTY pair and JSON mode disabled.
- One-shot, JSONL, and non-TTY behavior remains end-of-turn and keeps the current output contract.
- Tool display callbacks are synchronous and optional; existing registry calls without a callback preserve current behavior.
- Do not add external dependencies.
- Do not create an initial git commit in this worktree.

## Review Focus

- Existing Go Markdown output is not the parity oracle. Port the behavior covered by upstream `tests/render/test_markdown.c`, including inline and block parsing, list normalization, soft/hard breaks, em-dash spacing, wrapping, links, GFM tables, bounded table collection, and style transitions; preserve it in Go regression fixtures.
- A UTF-8 rune split across provider chunks must render once and remain valid: test in `internal/terminal/markdown/stream_test.go`.
- Definite prose must be emitted during `Feed`, before `Close`, while ambiguous syntax and tables remain buffered as upstream does: test in `internal/terminal/markdown/stream_test.go`.
- A retry after visible text must mark and reset only the visible attempt: test in `internal/terminal/live/render_test.go`.
- A table split across many stream chunks must remain buffered and render once at its boundary: test in `internal/terminal/markdown/stream_test.go`.
- A tool that streams bytes and returns a final summary must not duplicate user-visible output: test in `internal/tools/registry_test.go` and `internal/app/app_test.go`.
- A terminal writer failure must stay outside conversation history and stop later loop work: test in `internal/app/app_test.go`.

---

### Task 1: Incremental Markdown stream API

**Files:**
- Create: `internal/terminal/markdown/stream.go`
- Create: `internal/terminal/markdown/stream_test.go`
- Modify: `internal/terminal/markdown/render.go`

**Interfaces:**
- Produce `type Stream struct` with `NewStream(width int, styled bool, write func(string) error) *Stream`, `Feed(chunk string) error`, `Boundary() error`, `Reset() error`, `Close() error`, and `InTable() bool`.
- `Feed` accepts arbitrary byte chunk boundaries, including partial UTF-8 sequences, and emits definite prose eagerly. Ambiguous suffixes and tables remain buffered until resolved. `Boundary` flushes the current parse state and leaves the stream reusable. `Reset` discards buffered parser/wrapper/table state without writing. `Close` flushes remaining content exactly once and is idempotent.
- `Feed` eagerly emits unwrapped plain prose and defers ambiguous Markdown; width-constrained rendering and exact upstream parser parity are completed in Task 5. `Boundary` flushes the current segment and leaves the stream reusable. `Reset` discards buffered parser state. `Close` flushes remaining content exactly once.
- `Render(input, width, styled)` must use the same stream entry point so static and incremental paths share lifecycle behavior. The stream tests compare aggregated output with `Render` across the supported fixtures and chunk partitions.

- [x] **Step 1: Add failing stream tests** for eager prose, inline emphasis split across feeds, tables, partial UTF-8, reset, writer failure, and the em-dash/setext upstream regressions already in scope.
- [x] **Step 2: Run `go test ./internal/terminal/markdown -run '^TestStream' -count=1`** and confirm the new API or expected behavior fails.
- [x] **Step 3: Implement the stream lifecycle and eager unwrapped prose path**; defer ambiguous Markdown and buffer tables until their boundary.
- [x] **Step 4: Route `Render` through `Stream`; run the Markdown package tests and verify fixture output, writer-error latching, and feed-partition invariance.**

### Task 2: Tool presentation metadata and live display sink

**Files:**
- Modify: `internal/tools/registry.go`
- Modify: `internal/tools/bash.go`
- Modify: `internal/tools/files.go`
- Modify: `internal/tools/write.go`
- Modify: `internal/tools/registry_test.go`
- Modify: `internal/tools/bash_test.go`

**Interfaces:**
- Add `type DisplaySink func([]byte) error`.
- Add `type ToolOutputStyle uint8` with `OutputPlain` and `OutputUnifiedDiff` values, and `type ToolPreviewMode uint8` with `PreviewHead`, `PreviewHeadTail`, and `PreviewCollapsed` values.
- Add `type ToolPresentation struct` with `Argument string`, `Extra string`, `OutputStyle ToolOutputStyle`, `PreviewMode ToolPreviewMode`, `HeaderRows int`, and `CollapsedArgument string` fields. `Argument`, `Extra`, and `CollapsedArgument` are resolved from the original call arguments before local path or Bash preprocessing.
- Add `type ToolExecution struct` with `Result conversation.Item`, `Presentation ToolPresentation`, `OutputSummarizesDisplay bool`, and `HiddenTail int` for model-only suffixes.
- Add `Registry.Presentation(call conversation.Item) ToolPresentation` and `Registry.ExecuteWithDisplay(call conversation.Item, imageInput int, display DisplaySink) ToolExecution`; keep `Registry.Execute` as a wrapper returning `ToolExecution.Result`.
- Tool runners receive an optional `DisplaySink` as a third run parameter; the existing `Run` and `Execute` methods pass `nil` and keep their result contract. Read, write, edit, bash, and task-wait supply upstream-equivalent presentation metadata; bash resolves exploration preview mode from its command and task-wait formats its displayed argument and suffix before rendering.

- [x] **Step 1: Write failing tests** for metadata on all built-ins, bash exploration preview selection, task-wait argument suffix, write content streaming, bash/task-wait live output, and canonical result preservation.
- [x] **Step 2: Run the focused registry and bash tests** and confirm the display-aware API and callback assertions fail.
- [x] **Step 3: Implement metadata resolution and `ExecuteWithDisplay`**; adapt runners to emit display bytes only when a sink is present and mark summarized display output where upstream does.
- [x] **Step 4: Run focused tests** and verify callbacks receive exact visible bytes while returned conversation items remain unchanged.

### Task 3: Stateful live terminal renderer

**Files:**
- Create: `internal/terminal/live/render.go`
- Create: `internal/terminal/live/render_test.go`
- Use: `internal/terminal/markdown/stream.go`
- Use: `internal/terminal/width`
- Use: `internal/tools/registry.go`

**Interfaces:**
- Import `coretools` as `github.com/luank3/gax/internal/tools`.
- Produce `type Renderer` with `NewRenderer(writer io.Writer, width int, styled, showReasoning bool) *Renderer`, `Begin() error`, `Event(conversation.Event) error`, `Boundary() error`, `Retry() error`, `ToolStart(call conversation.Item, presentation coretools.ToolPresentation) error`, `ToolOutput([]byte) error`, `ToolResult(execution coretools.ToolExecution) error`, `Tick(now time.Time) error`, `HasRenderedAnswer() bool`, and `Close() error`.
- The renderer owns one `markdown.Stream`, tracks active mode, visible content, retry state, table buffering, and delayed status labels. Its first writer error is retained and returned by later methods; presentation errors never become conversation text.
- `Event` handles text/reasoning deltas, tool-call boundaries, retry/progress events, and stream completion/error. `Tick` advances delayed status labels without sleeping.

- [x] **Step 1: Write failing tests** for text deltas, hidden/shown reasoning, initial newline handling, event boundaries, retry after partial output, table status delay, tool start/output/result, completion, cancellation, and writer errors.
- [x] **Step 2: Run `go test ./internal/terminal/live -count=1`** and confirm the new renderer API is missing.
- [x] **Step 3: Implement `Renderer`** around `markdown.Stream` and Task 2's presentation types; track per-attempt state and render tool previews without duplicating streamed bytes.
- [x] **Step 4: Verify tests** assert exact output ordering, one unexpected-end marker per abandoned visible attempt, full tool presentation, and no writes after a latched error.

### Task 4: Wire rendering into the interactive TTY loop

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`
- Use: `internal/agent/loop.go` existing `Observe`, `TurnBegin`, `ToolSeen`, `ToolCall`, `Tick`, and `Checkpoint` hooks
- Use: `internal/terminal/live/render.go`
- Use: `internal/tools/registry.go` display-aware execution

**Interfaces:**
- The TTY loop creates one `live.Renderer` for each user turn and enables it only when `terminalinput.TTYPair` is true and JSON mode is false.
- `LoopOptions.Observe` forwards stream events; `TurnBegin` resets per-request state; `ToolSeen` renders the assembled tool call; `ToolCall` routes run/refuse/skip through the renderer and registry while returning the canonical tool result.
- A renderer error is checked by `Tick` and `Checkpoint`; after the loop closes the renderer, the app reports the original error and does not start another turn.
- When the renderer emitted the final answer, the post-loop answer writer is skipped. All other modes continue through the existing `writeAssistantAnswer` path.

- [x] **Step 1: Write failing app tests** with a pseudo-terminal and local SSE provider proving first text appears before stream completion, retry text is reset correctly, and final output is not printed twice.
- [x] **Step 2: Add tool integration tests** proving a live Bash result appears once and the same complete result remains in persisted session history.
- [x] **Step 3: Run focused app tests** and confirm they fail before wiring.
- [x] **Step 4: Wire the renderer through the existing loop hooks** with TTY/JSON gating and first-error propagation.
- [x] **Step 5: Run focused app tests** and verify one-shot, JSONL, and non-TTY output remains unchanged.

### Task 5: Full upstream Markdown parity

**Files:**
- Modify: `internal/terminal/markdown/render.go`
- Modify: `internal/terminal/markdown/scan.go`
- Modify: `internal/terminal/markdown/stream.go`
- Create: `internal/terminal/markdown/wrap.go`
- Create: `internal/terminal/markdown/table.go`
- Modify: `internal/terminal/markdown/scan_test.go`
- Modify: `internal/terminal/markdown/stream_test.go`

**Interfaces:**
- Replace the interim stream parser with the upstream behavior of `md_feed`, `md_flush`, `md_reset`, line scanning, inline parsing, wrapping, table layout/reflow, and style transitions in package-local Go code.
- Keep the `Stream` API from Task 1. Preserve eager output, feed-partition invariance, UTF-8 boundaries, writer-error latching, and reusable boundaries.
- Port the behavior covered by `/home/dat/dev/hax/tests/render/test_markdown.c` into checked-in Go fixtures, grouped by pass-through, block/inline syntax, feed partitioning, hard/soft breaks, wrapping, links, tables, styles, and width behavior. Tests must not require the C checkout at runtime.

- [x] **Step 1: Add failing Go regression fixtures** for upstream behavior missing from the interim renderer, including eager wrapped prose, block and inline state across feeds, cursor-safe wrapping, link wrapping, table caps/reflow/alignment, and style transitions.
- [x] **Step 2: Run `go test ./internal/terminal/markdown -count=1`** and confirm the parity fixtures fail.
- [x] **Step 3: Port the upstream state machine, wrapper, and table layout/reflow** while retaining the public Go API.
- [x] **Step 4: Run all Markdown tests** and verify upstream fixture output and feed-partition invariance.

### Task 6: Repository verification and parity-plan updates

**Files:**
- Modify: `docs/superpowers/plans/2026-10-10-command-entrypoint.md`
- Modify: `docs/superpowers/plans/2026-10-10-file-tools.md`
- Modify: `docs/superpowers/plans/2026-10-10-bash-task-tools.md`

- [x] **Step 1: Run `go test -race -count=1 ./...`** and resolve failures.
- [x] **Step 2: Run `go vet ./...` and Linux amd64 and macOS arm64 builds** and resolve failures.
- [x] **Step 3: Mark only the live-rendering and display-hook plan items complete**; leave unrelated terminal, slash-command, compaction, usage/cost, output-drain, and task-report work open.
