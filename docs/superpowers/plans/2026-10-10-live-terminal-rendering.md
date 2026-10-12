# Live Terminal Rendering Implementation Plan

> Status-row correction (2026-10-12): a status row painted on the row an answer's own newline
> would have taken used to drop that newline from the count `clearStatus` restores, so the next
> block separator (`openBlock`) added a second blank line. The visible effect was two blank lines
> between a streamed answer and the tool row that followed it, whenever the delayed `working...`
> indicator appeared between them. `statusLineNewlines` now records the count the paint's own
> newline commits (`internal/terminal/live/render.go`), covered by
> `TestStatusRowLeavesOneBlankLineBeforeTheToolHeader`, which pins one blank line before `[bash]`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render streamed assistant output and built-in tool calls live in the TTY REPL while leaving canonical conversation history unchanged.

**Architecture:** Make the Markdown engine incremental and use it for both static and live Markdown. Add a stateful terminal renderer that consumes provider events, per-call tool presentation metadata, and a display sink in the tool registry; wire both paths only into the TTY REPL. One-shot, JSONL, and non-TTY output keep their current contracts.

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

- Existing Go Markdown output is not the oracle. Pin the intended behavior with checked-in fixtures covering inline and block parsing, list normalization, soft/hard breaks, em-dash spacing, wrapping, links, GFM tables, bounded table collection, and style transitions; preserve it in Go regression fixtures.
- A UTF-8 rune split across provider chunks must render once and remain valid: test in `internal/terminal/markdown/stream_test.go`.
- Definite prose must be emitted during `Feed`, before `Close`, while ambiguous syntax and tables remain buffered: test in `internal/terminal/markdown/stream_test.go`.
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
- `Feed` eagerly emits unwrapped plain prose and defers ambiguous Markdown; width-constrained rendering and exact parser parity are completed in Task 5. `Boundary` flushes the current segment and leaves the stream reusable. `Reset` discards buffered parser state. `Close` flushes remaining content exactly once.
- `Render(input, width, styled)` must use the same stream entry point so static and incremental paths share lifecycle behavior. The stream tests compare aggregated output with `Render` across the supported fixtures and chunk partitions.

- [x] **Step 1: Add failing stream tests** for eager prose, inline emphasis split across feeds, tables, partial UTF-8, reset, writer failure, and the em-dash/setext regressions already in scope.
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
- Tool runners receive an optional `DisplaySink` as a third run parameter; the existing `Run` and `Execute` methods pass `nil` and keep their result contract. Read, write, edit, bash, and task-wait supply equivalent presentation metadata; bash resolves exploration preview mode from its command and task-wait formats its displayed argument and suffix before rendering.

- [x] **Step 1: Write failing tests** for metadata on all built-ins, bash exploration preview selection, task-wait argument suffix, write content streaming, bash/task-wait live output, and canonical result preservation.
- [x] **Step 2: Run the focused registry and bash tests** and confirm the display-aware API and callback assertions fail.
- [x] **Step 3: Implement metadata resolution and `ExecuteWithDisplay`**; adapt runners to emit display bytes only when a sink is present and mark summarized display output where the renderer expects it.
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

### Task 5: Full Markdown parity

**Files:**
- Modify: `internal/terminal/markdown/render.go`
- Modify: `internal/terminal/markdown/scan.go`
- Modify: `internal/terminal/markdown/stream.go`
- Create: `internal/terminal/markdown/wrap.go`
- Create: `internal/terminal/markdown/table.go`
- Modify: `internal/terminal/markdown/scan_test.go`
- Modify: `internal/terminal/markdown/stream_test.go`

**Interfaces:**
- Replace the interim stream parser with the full behavior of line scanning, inline parsing, wrapping, table layout/reflow, and style transitions in package-local Go code.
- Keep the `Stream` API from Task 1. Preserve eager output, feed-partition invariance, UTF-8 boundaries, writer-error latching, and reusable boundaries.
- Port the reference behavior into checked-in Go fixtures, grouped by pass-through, block/inline syntax, feed partitioning, hard/soft breaks, wrapping, links, tables, styles, and width behavior. Tests must not require the C checkout at runtime.

- [x] **Step 1: Add failing Go regression fixtures** for behavior missing from the interim renderer, including eager wrapped prose, block and inline state across feeds, cursor-safe wrapping, link wrapping, table caps/reflow/alignment, and style transitions.
- [x] **Step 2: Run `go test ./internal/terminal/markdown -count=1`** and confirm the parity fixtures fail.
- [x] **Step 3: Port the reference state machine, wrapper, and table layout/reflow** while retaining the public Go API.
- [x] **Step 4: Run all Markdown tests** and verify fixture output and feed-partition invariance.

### Task 6: Repository verification and parity-plan updates

**Files:**
- Modify: `docs/superpowers/plans/2026-10-10-command-entrypoint.md`
- Modify: `docs/superpowers/plans/2026-10-10-file-tools.md`
- Modify: `docs/superpowers/plans/2026-10-10-bash-task-tools.md`

- [x] **Step 1: Run `go test -race -count=1 ./...`** and resolve failures.
- [x] **Step 2: Run `go vet ./...` and Linux amd64 and macOS arm64 builds** and resolve failures.
- [x] **Step 3: Mark only the live-rendering and display-hook plan items complete**; leave unrelated terminal, slash-command, compaction, usage/cost, output-drain, and task-report work open.

## Follow-up fixes

Fuzzing the Markdown stream (`FuzzStreamFeedNeverLatchesOrCorrupts`) and benchmarking `Render`
surfaced three defects in the ported renderer. The first two are fixed; the third is recorded as
a known gap.

- [x] Bound the URL scheme comparison. `bareURL` lowercased the entire remaining input once per
  character, which made the inline pass quadratic: a 64 KiB response took ~50s to render with
  invalid UTF-8 and ~0.75s as plain prose. Comparing a scheme-length slice is O(1) and restores
  linear rendering (~25 MB/s).
- [x] Make eager emission safe. The re-render design emitted a prefix whenever the scanner settled
  it, but the withheld tail or the next byte could re-render the boundary, so `flushSegment`
  latched "markdown stream output changed after it was emitted" and the live renderer aborted the
  answer stream. Emission now requires the bytes to survive the buffered input and both a content
  and a termination probe, and `stream_test.go` pins the CR and vertical-tab cases.
- [x] Emit a heading's terminating newline when the heading line closes
  (`markdown.c:772`), instead of deferring it into the next block's separator. Deferring it lost
  the break whenever the output already ended in a newline, so a heading after a table rendered as
  `...m\n0` where the heading's break is supplied plus the blank line it owes the next block.
  The blank-line debt is now one newline relative to the closed heading rather than a target count
  of stripped trailing newlines, which could not see the heading's own (glyph-less) row.
- [x] Make the inline renderer incremental and unify the two entry points. `inlineState` now
  walks input once and stops wherever a later byte could change the meaning
  (`renderResumable` in `inline.go`), deferring em dashes, URL schemes, inline-code runs, emphasis
  runs, and trailing whitespace or carriage returns; `prevByte` carries the left neighbour across
  a chunk boundary, which the emphasis and link rules read. The batch renderer is now the
  `final=true` entry point of that same walker, so streaming and whole-document rendering share one
  implementation and cannot disagree. Verified by `TestInlineResumable*`: the hand-written corpus
  plus 30k generated inputs, both styled modes, each fed in growing windows against the batch
  result. All 93 Markdown tests, including the regression fixtures, still pass.
- [x] Resynchronize instead of latching. A contradicted emission is a boundary artifact of
  re-rendering, not a reason to lose the answer: `flushSegment` now continues from the last byte
  the incremental output and the render agree on. Latching reached the live renderer's checkpoint
  and aborted the whole turn. Measured over 20k generated documents x 3 chunkings: latches
  11/60000 -> 0/60000, divergences 0 -> 11/60000 with a worst-case artifact of 3 bytes. Guarded by
  `TestStreamChunkingCensus`, which fails if any chunking diverges.
- [x] Replace the block-level re-render. Done: `parser.go` is the only renderer, `Stream` is a
  thin adapter over it, and the re-render path is deleted (`emitEagerPlainPrefix`,
  `unsafeEagerRune`, `hasDefiniteBlockAfter`, `trailingHardBreak`, `tableEnded`,
  `partialTableOverflow`, `flushSegment`, `commonPrefix`, `renderDocument`, and the
  `joinParagraph*` family). Streaming is linear: 64 KiB at 64 B chunks went from 2.59 s to 13.4 ms,
  8 KiB chunks 27 ms to 1.07 ms, one feed 5.9 ms to 1.0 ms, and both censuses report zero
  divergences (0 / 60000 streaming, 0 / 8000 generated) with zero latches. The resync from the
  previous item went with it, since nothing can contradict an emission that is settled before it
  is written.
- [x] Close the residual segment-composition gap. Carrying the separator state across a boundary is
  what does it: `Boundary` resolves the held bytes against end of segment without discarding the
  parser, keeping `pending_heading_blank`, so a heading that ends a
  segment still pays the blank line it owes the next block. `TestStreamSegmentBoundaryAfterHeadingKnownGap`
  reported the match and was deleted per its own instruction; the fuzz target now asserts that a
  split render equals the whole-document render.

### Evidence for choosing the fix

A divergence census (20k generated Markdown-ish documents x 3 random chunkings, fixed seed) and
the streaming benchmarks measure what the re-render design costs:

| Measurement | Result |
| --- | --- |
| Turn-aborting latch errors | 11 / 60000 (0.018%) |
| Silent divergences from `Render` | 0 / 60000 |
| 64 KiB prose, one feed (`Render`) | 6.7 ms |
| 64 KiB prose, 8 KiB chunks | 27 ms |
| 64 KiB prose, 1 KiB chunks | 194 ms |
| 64 KiB prose, 64 B chunks | 2.9 s |

Two conclusions. Silent divergence is now zero in that distribution, so the only known output gap
is the pinned heading blank — carrying one flag closes it. But the latch class survives at 0.018%,
and each latch reaches `Renderer.failure()` through `Event`/`boundary` and is sampled by the app's
`Checkpoint`, which returns `SignalAbort`: the answer stream dies mid-turn. That class cannot be
closed by carrying separator state, because it is caused by the eager path emitting a prefix a
later chunk contradicts, and the probe only samples two continuations of the buffered input.

The benchmark also shows the per-feed cost is inversely proportional to chunk size — the signature
of re-rendering the whole segment on every `Feed` (O(n^2/chunk)). The design to reach is linear: a
step function that processes only `tail + chunk`, with the tail capped at `TAIL_MAX`.

Sizing the parser port against the existing tree:

| Reference unit | Lines | Go equivalent | Lines | Reusable as-is |
| --- | --- | --- | --- | --- |
| emit, inline, dispatch | 1216 | `render.go` | 1187 | inline + emit logic, not the batch loop |
| line scan | 170 | `scan.go` | 190 | yes |
| wrapping | 385 | `wrap.go` | 297 | needs incrementalization |
| table layout | 548 | in `render.go` | - | table layout, not collection state |

So the port is mostly re-plumbing what exists: convert the batch loop into a resumable state
machine carrying the same ~20 fields, add the bounded deferred tail, and emit through a callback
instead of accumulating into a `strings.Builder` (which also deletes `newlineCount`/`stripANSI`,
the source of the heading bug).

### Resuming the block-level parser

The remaining piece is a resumable block state machine that replaces `Stream`'s re-render. A
working draft was built and then removed rather than left unwired; these are the findings, which are
the expensive part to rediscover.

Shape: consume complete lines from a `pending` buffer; stream paragraphs through
`inlineState.renderResumable` plus one persistent `terminalWrapper`; buffer every other block and
render it once with `renderDocument` when the line that ends it arrives; and let the parser own the
separators.

Solved while drafting it:

- Eager streaming and the join ordering conflict. A paragraph line's bytes cannot be written before
  the separator that precedes them is known, so the separator is computed when the line completes
  and *held* until the next line's first bytes arrive. A soft break needs no separator when the
  previous line already ended in whitespace, because those bytes are already out.
- A held trailing carriage return. The batch trims it from a line the paragraph continues past and
  re-emits it only for open inline code or a hard break, and keeps it for the paragraph's last
  line — so it must be held and resolved when a continuation arrives, or at paragraph close.
- A partial line cannot be streamed until the scanner places it in the paragraph; otherwise a line
  that turns out to be a block start is written as prose.
- The first line of a paragraph owes the same treatment as later ones (separator, held return,
  recording for the four-space continuation rule).
- `ensureNewlines` must write nothing before any content exists, and the batch's separator count is
  per paragraph, not per document.

Still open, with the batch rules that define it:

- Leading-pipe lines. The batch decides between rendering a table, rendering the line inline, and
  emitting the remaining lines literally using lookahead over the following lines
  (`hasTableLookahead`, `parseTable`). A parser that delegates the accumulated block to
  `renderDocument` cannot see that context, so the table/pipe fallbacks diverge. This needs the
  decision to move into the parser.
- End-of-input fallbacks: the short fence candidate after prose (`paragraphRawTail`), the
  whitespace-only continuation, the indented incomplete fence, and the rejected table row.
- Subtle ordering where a held carriage return, the separator, and the inline walker's state
  interact inside a styled run.

### How other streaming parsers solve this (research, 2026-10-11)

Three independent surveys — incremental CommonMark parsers, terminal streaming renderers, and the
streaming-parser literature — agree on one rule: **never emit bytes you may have to retract.** Every
production parser either holds the ambiguous construct unemitted until a disambiguating line
arrives, or buffers whole blocks:

| Implementation | Strategy at an ambiguous boundary |
| --- | --- |
| `micromark` (streaming state machine) | buffers the first line's events, `check`s the next line for a setext underline or table delimiter row, then splices the buffered events before any exit event is emitted. Invariant: no token is final until its matching `exit` event. |
| `cmark-gfm` `cmark_parser_feed` | buffers into `curline`, dispatches only complete lines, and mutates the AST retroactively (a paragraph becomes a table/heading when the delimiter row or underline arrives); nothing is serialized until `cmark_parser_finish`. |
| `pulldown-cmark`, `comrak`, `md4c`, `goldmark`, `markdown-it` | batch: whole document, next-line peeks inside an immutable buffer, output deferred to AST traversal. |
| `glamour`/`glow`, `mdcat`, `termimad` | batch AST; tables cannot stream at all because column widths are global. |
| `mdflow` (md4c-based streaming) | bounded lookahead; tables are redrawn in place with cursor moves when a wider row arrives. |
| `llm-ui`, `Streamdown` | "stable head, streaming tail": commit blocks whose `\n\n` boundary closed, keep the last block mutable. Leading-pipe lines stay uncommitted until the delimiter row confirms. |
| `rich.live` | re-parse + ANSI rewind — documented as O(N²) with scrollback corruption when content exceeds the viewport. |

The formal statement of the same rule is prefix-monotone emission: emit only the longest common
prefix of *all* completions of the input so far (the "stem" of the NFA path tree; Grathwohl,
Henglein & Rasmussen, *Optimally Streaming Greedy Regular Expression Parsing*, arXiv:1704.08820),
with bounded-tail commit points (Mizushima et al., *Packrat Parsers Can Handle Practical Grammars
in Mostly Constant Space*, doi:10.1145/1806672.1806679) and elastic sliding windows (Kuramitsu,
doi:10.2197/ipsjjip.23.505) as the memory bound.

Two consequences for this work:

1. **`STEP_DEFER` plus a bounded tail is the correct design, not a workaround.** The step function
   is a faithful instance of it: the line scan reports `classification_complete`, each step returns
   `ADVANCED`/`DEFER`/`PASS`, and the work buffer holds at most `TAIL_MAX` bytes beyond the current
   chunk. The current `commonPrefix(input, input+"x", input+"\n")` probe samples two completions
   out of infinitely many, which is why divergence is 11/60000 rather than zero, and why the resync
   exists at all.
2. **The design to abandon is the one that failed**: a block state machine that accumulates a block
   and delegates it to `renderDocument`. Delegation is what forced document-level lookahead
   (`hasTableLookahead`) into a streaming decision. The step design never delegates: it decides at
   the byte that makes the decision possible, and otherwise defers.

So the target is not "a block parser plus the inline walker". It is a single step loop over
`tail + chunk`, which already exists in the tree in pieces:

| Piece | Role | Status |
| --- | --- | --- |
| step loop + tail/`TAIL_MAX` | work buffer, deferred tail, `final` threading | missing |
| line start | block dispatch from `at_line_start` | logic exists in `render.go`'s batch switch |
| inline step | inline bytes, pass-through to block markers | logic exists in `inline.go` |
| code fence / inline code states | fenced and inline code accumulation | logic exists in `render.go` |
| table start, step, bail, finish | two-complete-lines start rule, row collection, oversize bail, EOF last chance | replaced by batch `hasTableLookahead`/`parseTable` |
| flush | EOF: table last chance, `final` pass, close open styles, wrap flush | `Stream.Close` |
| styled-mode seam | seam semantics for a styled-mode switch | `Stream.SetStyled` |
| in-table probe | spinner while silently accumulating | `Stream.InTable`, currently probing |

The four "EOF fallback" gaps are `final`-flag branches in that loop, not special cases: the line
start emits literally when `final` and the classification never completed, defers when an indented
prefix is incomplete, the step loop emits the residual tail literally when `final`, and the flush
gives a collecting table its last chance and emits a rejected final row literally
(`was_collecting_table`). The leading-pipe gap disappears because the table start needs two
*complete lines in the work buffer* and returns `DEFER` otherwise — no document context.

Keep the conformance compromises: setext headings, reference-style links, and footnotes need
unbounded lookahead, and every streaming renderer surveyed gives them up the same way (never
retro-transform earlier lines). `ScanLine(line, final)`/`LineInfo.Complete` already carries the
completeness signal the `final` branches need, and `inline.go` is already resumable, so this is
re-plumbing, not new parsing logic.

Revised order of work:

1. `process(work, final)` skeleton: `tail` with `TAIL_MAX`, `at_line_start`, `at_blank`,
   `pending_heading_blank`, `prev_byte`, paragraph streaming through `renderResumable`, and
   `renderDocument` used only for blocks whose decision is already complete.
2. Port the table state machine; delete `hasTableLookahead` and `parseTable`'s batch lookahead.
3. Port the four `final` branches.
4. Wire as the only renderer: `Render` becomes one feed plus `Close`; delete
   `emitEagerPlainPrefix`, the probe loop in `flushSegment`, `renderDocument`'s batch loop, and the
   resync from 2b (it becomes unreachable).
5. Tighten the gate: `TestStreamChunkingCensus` asserts divergences are zero, not ≤ total/1000, and
   `feedOnce(input) == Render(input)` for every fixture and chunking.
6. Re-run the chunked benchmarks: the per-feed cost should stop scaling with the number of chunks
   (64 KiB at 64 B chunks is 2.6 s today against 6.6 ms for one feed).

### Progress: the parser exists and matches the batch (2026-10-11)

Steps 1-3 are implemented in `parser.go` (unwired): `feed`/`finish`, the separator state
(`hadContent`, `afterBlank`, `afterHeading`, `trailingNewlines`), prose streaming with the
one-line boundary hold, the table start/collect/finalize machine, fenced code, headings with their
held italic close, thematic lines, list items, blockquotes, leading-pipe fallbacks, and the
end-of-input branches. `wrap.go` gained `write`/`drain` so a block can stream through the same
wrapper `wrapTerminal` builds, and `wrapTerminal` now uses them.

`parser_test.go` gates it against `renderDocument`: the hand-written block and inline corpus at
four widths in both styled modes, and every input re-fed one byte at a time and in random splits —
all byte-identical. A generated census over the streaming alphabet still reports 28 / 8000
divergences (0.35%), all end-of-input interactions where a carriage return or tab meets a
separator; the test records the rate and will tighten to zero at step 5.

What the port settled, in case it is re-derived: a line's boundary — whether the paragraph
continues, so whether the line is trimmed and what separator follows — needs the next line's first
bytes, exactly as the newline handler does; everything else about a line is line-local.
Prose therefore streams as it arrives while only its trailing whitespace run, a trailing backtick
run, and a whitespace-or-backtick-only partial line wait. A non-positive width must bypass the
wrapper entirely (it expands tabs to four spaces and keeps carriage returns), and a partial line
mid-stream is scanned with `final=false`: only a line at end of input takes the final reading.

The generated census now reports **0 / 8000 divergences**, so the parser reproduces the batch
renderer exactly on everything the gate covers. Two batch bugs surfaced on the way and are fixed:
the raw-tail join was applied to a whitespace-only final line, where the inline state keeps the
rendering and emits only the newline, and a paragraph line whose classification is still unsettled
(`LineIncomplete`, such as a bare `0`) starts a new block instead of continuing the paragraph.

### Wiring the parser as the only renderer (landed)

Three defects had to be fixed before the conversion held, and all three were real:

- The wrapper counted cells per call, so a rune split across two feeds was counted as two cells and
  a row broke in the wrong place (`"héllo wörld"` split after two bytes). It now holds an
  incomplete trailing rune, and `finish()` resolves it at the end of a block.
- A valid fence closer at end of input must close the fence rather than be copied into the body.
- An oversized partial table row bails the attempt during the feed, as the
  `md_table_bail_partial` does, instead of being retained until Close.

Two deliberate behavior changes came with it: prose is emitted only up to the bytes a later byte
cannot change, so a trailing space arrives with the byte that decides it, and a line whose
classification is not settled yet is not joined to the paragraph (joining it early left Close
unable to re-read it, which made the render depend on chunking).

`parser.feed(input, true)` with a non-empty input used to differ from `feed(input, false)` followed
by `feed("", true)` for a list item whose last line is a partial bullet-ish line (`"01. \`\n* "`).
A final feed now settles its bytes as ordinary input first and only then resolves the tail, so the
two shapes agree by construction, pinned by `TestParserFinalFeedMatchesStreamingShape`.

Fuzzing the finished port: 612k executions of the strict stream invariant (a split render must
equal the whole-document render, valid input must stay valid UTF-8, no feed or close may fail) and
6.8M of the close/boundary lifecycle contract, both clean.

The earlier note on the reverted attempt follows.

### Wiring the parser as the only renderer (attempted, reverted)

`Stream` was converted to an adapter over the parser: `Feed` → `parser.feed(chunk, false)`,
`Boundary` → `parser.feed("", true)` then a fresh parser (which is what the re-render design
already does: resolve the segment as a complete document, then start again), `SetStyled` →
`Boundary` under the old mode, `Reset` → a fresh parser, `Close` → `feed("", true)`,
`InTable` → `parser.mode == modeTable`. That shape works, but eight `stream_test.go` cases pin
behavior the parser deliberately changes, so the conversion was reverted rather than left failing:

- Five cases assert the old *timing*: prose is now emitted only up to the bytes a later byte cannot
  change, so a trailing space or the newline that ends a line arrives with the following bytes
  (`"alpha "` becomes `"alpha"` before the next feed; `"Intro\n"` becomes `"Intro"`). The byte
  stream is identical at a boundary; only the moment differs. These assertions need updating.
- `TestStreamOutputMatchesRenderAcrossChunkPartitions` found a real difference: with
  `"héllo wörld"` split after two bytes, the stream wraps one character earlier than a
  whole-document render (`w` vs `wö`). The wrapper reflows the current row, and it is fed
  incrementally here, so its break point can differ from a single-pass wrap. Worth fixing in the
  wrapper (or by holding the row until a word completes) rather than accepting.
- `TestStreamCloseResolvesEOFStyles` expects an unterminated fence's trailing partial
  closer to be dropped (`\x1b[2mfoo\n\x1b[22m`), and the parser emits it as body.
- `TestStreamBailsOversizedUnterminatedTableRowLosslessly` expects the literal header and row body
  of an oversized unterminated table attempt, which the parser's verbatim path did not produce.

Until that is done, `renderDocument` remains in the tree as the re-render path's renderer and as
the parser's differential oracle; deleting it and converting `parser_test.go`'s corpus gate to
`feedOnce(input) == Render(input)` is the last step.

### Settling the parser against the reference (2026-10-11)

The batch renderer is deleted, so the suite can no longer flag a block-rule divergence on its own.
A throwaway driver built from the reference implementation's library renders stdin at a given
width (`md_new(capture, &out, wrap_width)`, `md_feed`, `md_flush`):

```sh
cc -iquote <reference>/src -std=c11 -O1 -D_POSIX_C_SOURCE=200809L -D_XOPEN_SOURCE=700 \
   -o /tmp/mdrender /tmp/mdrender.c <reference>/build/libhax_lib.a -lcurl -ljansson -lm -lpthread
printf 'input' | /tmp/mdrender <width>
```

Four inputs where the parser and the deleted batch renderer disagreed were checked against the
reference driver at widths 0, 8, 20 and 40:

| Input | Reference | Parser |
| --- | --- | --- |
| `- **)word\n 0\n\n` | as the parser | matches |
| ``01. `\n* `` | as the parser | matches |
| ``1. \r\n`\n`` | `` ` `` literal, item text keeps its CR at width 0 | renders an empty inline-code span |
| `===- **b—\r\r\n*` | the tail stays inside the open bold; wraps with a newline at width 8 | closes bold before the tail; reflows with a cursor move |

So the two inputs the batch renderer got wrong are now right, and the other two are open:

1. A short ambiguous prefix after a list item (``1. \r\n`\n``) must fall back literally like a
   paragraph's does. The `!p.itemEnd` gate in `stepProse` skips the end-of-input rules for items;
   removing it fixes this input and breaks ``01. `\n* ``, which the reference renders as the parser
   now does. The item path needs the reference rules rather than either gate.

   **Fixed.** The gate is gone, and the deferral now matches the reference: a line whose streaming scan
   is unsettled waits for one more byte whether or not it is newline-terminated, and at end of input
   a line the final scan calls text is emitted literally instead of inline-parsed. The hold requires
   the final scan to be text, so a blank line still ends the paragraph with its two newlines, and a
   terminated last line still owes its trailing newline. Checked against the C at widths 0, 8, 20 and
   40: ``1. \r\n`\n``, ``- **)word\n 0\n\n``, ``01. `\n* ``, ``- a\n ``, ``- a\n` ``, ``abc\n`\n``,
   ``abc\n`\nxyz\n``, ``abc\n```\nxyz\n``, ``\n``, ````\n`` and `a\n\nb\n` all match; 4 of 34
   probed combinations still differ, all of them the next item.
2. A deferred tail must be emitted *inside* the inline run it sits in, with that run closing after
   it, which is what the reference's newline handling produces. The walker closed the run
   first, so the tail landed outside it. **Fixed** by giving the walker a literal region: the parser
   marks the resolved tail with `inlineLiteralTail` (`'\x01'`, the sibling of the line sentinel) and
   the walker copies it verbatim, so an open run still closes after it. `===- **b—\r\r\n*` now
   matches the reference at widths 0, 8, 20 and 40 — the width-8 case included, because the tail is part
   of the run the wrapper measures.

Both are pinned by `TestRenderKeepsDeferredTailInsideOpenRun`, whose expected bytes were taken from
the reference driver rather than from the parser. The probe list above now reports 40 of 40
combinations matching.

### Reference sweep: how faithful the parser is (2026-10-11)

The driver makes the whole parser measurable, not just the cases that came up. 300 generated inputs
from the streaming census alphabet, at widths 0 and 20 (600 cases), rendered by the reference and by
the parser:

| Result | Count |
| --- | --- |
| Identical | 585 / 600 after the rules below; 559 before |
| Differ only in whitespace | 13 |
| Differ in content | 2 |

Four of the rules have since been ported (carriage returns kept, the heading's
indentation and tab expansion, no closing-hash stripping, and hashes followed by
a newline staying prose), each verified against the driver; the remaining
whitespace cases and the nested-emphasis close order are still open.

The whitespace class is the parser's carriage-return and tab policy: a continued line's `\r` is
trimmed where the reference keeps it as a text byte, a heading's leading tab is trimmed where the
reference expands it, and trailing spaces before a break follow the same split. The content class is
two rules: `trimClosingHashes` strips a heading whose text is only hashes (`## # ` renders empty
where the reference keeps `# `), and a nested-emphasis case (`> *****http://x **\x0b`) closes bold
and italic in a different order.

All of these are port deviations rather than parser regressions: they live in rules the deleted
batch renderer shared (`joinParagraphWithOptions`' `previousWithoutCR`, the heading `TrimLeft`,
`trimClosingHashes`), and the parser-vs-batch comparison over the same alphabet found four inputs,
all fixed above. Separating the two properly needs the batch renderer reconstructed out-of-tree
(`git archive HEAD`), which is what the review did for those four; the sweep above measures the
parser against the reference and cannot attribute by itself.

Method, for repeating it: write `"<base64 input> <width>"` per line to `/tmp/corpus.txt`, render
each line with `/tmp/mdrender <width>`, render the same lines through `Render(input, width, true)`
in a throwaway test that writes base64 to `/tmp/go_out.txt`, then compare.

### Are the deviations deliberate? (2026-10-11)

The five rules above could be intentional choices rather than gaps, so the port's own fixtures were
run through the same driver: 21 fixture inputs from `stream_test.go`, at their own widths, in both
styled modes. **20 of 21 match the reference byte for byte.** The one that does not is the
carriage-return case (`foo\r\nbar`), and no test in the package asserts the parser's trimmed form as
the intended behavior — the CR tests (`TestStreamMatchesRenderForCRLines`, the fixture list entry)
compare the stream against `Render`, which is self-consistency, not parity.

So the deviations are gaps rather than deliberate choices: wherever the parser has a
reference-derived fixture it matches, and the rules it lacks were simply never ported. Fixing them
is therefore conformance work, not a taste question. Each has a reference-verified repro, and any
fixture whose expected bytes encode the parser's form of a rule must be corrected to the reference
form in the same change.

### Structured corpus: exact on realistic documents (2026-10-11)

The adversarial alphabet above barely exercises tables, fences and real lists, so 45 hand-written
documents were run through the same driver at widths 0, 20 and 40 (135 cases): headings with
closing hash runs and tabs, paragraphs with emphasis, nested emphasis, inline code and bare URLs,
hard breaks and trailing spaces, bullet and ordered lists including nesting and continuations,
blockquotes, thematic breaks, setext-style underlines, fenced code with info strings, nested
backticks, empty and unterminated fences, tables with alignment rows, ragged rows, inline styling
inside cells, a row wider than the width and a table wider than the column cap, mixed documents,
CRLF line endings, unclosed emphasis and code, and runs of blank lines.

**0 of 135 differ.** The parser is exact on realistic Markdown; the 15 / 600 residual in the
adversarial sweep is confined to degenerate shapes that corpus does not produce.

### Parked status below an open read row (2026-10-12)

A `read` header stays open so later reads coalesce onto the same row, but the delayed status
indicator was painted one row below it with no way back: clearing it left the cursor at column zero
of the row it had borrowed, so the next `, file:range` suffix landed there. Reads separated by a
provider round trip — the common case, since the indicator only appears after 300 ms of waiting —
rendered as

```
[read] mock.go:445-574
, registry_test.go:1-100
, transcript.go:1-230
```

The ported renderer parks the indicator instead: it draws the
label two rows below an open row and records the row's column, and hiding it (`erase_locked`)
erases the label row, climbs back, and restores that column, so the next read still coalesces. The
port now does the same — `Tick` writes the two-row park and `clearStatus` climbs back — and
`closeCollapsedCluster` clears the indicator first, exactly as the parked-mode close does
(`src/render/render_ctx.c:21`) hides the spinner before terminating an open row.
`TestRendererParksStatusBelowOpenCollapsedReadRow` pins the byte stream: `\n\n` before the label,
then `\r\x1b[2K\x1b[2A\x1b[13G` before `, b.txt`. The row the indicator borrows is the gap the
block separator owes the next block anyway, so the committed layout is unchanged.

### Flicker-free status frames and a hidden cursor (2026-10-13)

The status row flickered, and the terminal cursor sat blinking at the
end of the row (and at the end of every streamed answer) for the whole turn.

**Why it flickered.** Every frame was four separate writes — `\r\x1b[2K`, then `\x1b[2m`, then the
glyph and label, then `\x1b[0m` — and the erase came *first*. `stdout` is the raw `*os.File`, so
each of those is its own `write(2)`: between the erase and the paint the terminal shows an empty
row, which a multiplexer or a slow link renders as a blink. The renderer builds one row and flushes it
once (`draw_label_row_locked` plus `finish_row_repaint`, `src/render/spinner.c:91`), and it erases
the row's *tail* (`ANSI_ERASE_LINE`, `\x1b[K`) after the paint rather than the row before it, which
is why its comment says the order avoids a blank frame.

A label change was worse: `setStatus` erased the visible row and restarted the 300 ms show grace,
so `working... → thinking... → working...`, each `processing... N%` progress event, and each
`retrying in Ns...` tick blanked the row for a third of a second. A label change must never erase:
the row is redrawn in place, and a pending label keeps the displayed one until it settles.
`PausePending` did the same erase, so pressing Escape
blanked the indicator before `pausing...` came back.

**Why the cursor was visible.** The REPL owns cursor visibility rather than the spinner: it shows
the cursor for the prompt and hides it again until the next one, so a turn — streamed answer, tool
output, animated indicator — runs with it off. The port never emitted DECTCEM, so the hardware
cursor parked at the end of the status row and followed the streamed text. A fatal signal has to
restore it, as the restore path does, or the parent shell inherits an invisible cursor.

**What changed.**

- `Tick` assembles one frame (`\r` + dim + glyph + space + label + reset + `\x1b[K`) and writes it
  once, and never erases the row before painting it.
- The show grace applies to a first appearance only; a label that replaces a visible one repaints
  over it on the next tick.
- `setStatus` and `PausePending` no longer clear on a label change — only the empty label hides the
  indicator. `clearStatus` assembles its erase, climb and column restore into one write.
- `runTTYREPL` shows the cursor before `ReadTTYLine` and hides it again for the turn, restoring it
  on every exit path; `watchTerminationEscalation` writes `\x1b[?25h` before re-raising a fatal
  signal.

`TestRendererRepaintsStatusInPlaceWithoutErasingFirst` pins the three-frame byte stream (no
`\r\x1b[2K` between frames) and `TestRendererParksStatusBelowOpenCollapsedReadRow` gains the tail
erase per frame; `TestRendererKeepsStatusWithinTerminalWidth` now measures the resolved screen,
since the tail erase occupies no cells. `TestRunTTYREPLHidesCursorForTheTurn` pins the PTY order:
hidden after the submitted prompt, restored only after the answer.

**Still open.** Indicator transitions should be wrapped in DEC 2026 synchronized output so an erase
and the writes that replace it land as one frame, and tool status should paint through the
spinner's own view instead of the renderer writing rows itself. Each transition is written as a
single write instead, which covers the indicator but not the erase-plus-replacement pair.
