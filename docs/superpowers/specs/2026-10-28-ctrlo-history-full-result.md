# Fix ctrl-o history view losing tool results

## Summary

Ctrl-O (`view the conversation in $PAGER`) rendered stored tool results through the
live-preview renderer with the same preview modes used while a tool runs. A `read`
call's result was therefore collapsed to a quiet breadcrumb, and any other result was
capped to a head/tail preview that dropped middle lines and truncated long lines. The
canonical output the model already received was not shown in the paged history view.

## Root cause

`internal/app/history_render.go:159` set the preview mode only for the brief replay
(`historyBrief` → `PreviewCollapsed`). The full paged view (`historyFull`) left the
mode at whatever `registry.Presentation(call)` returned, which for `read` is
`PreviewCollapsed` and for `bash`/`web_*`/`task_wait` is `PreviewHeadTail`. Those modes
are correct while a tool streams live, but in history the bytes are settled and the
stored result is the only copy the user can page through.

`internal/terminal/live/tool_render.go` honored that mode: `processLine` skipped the
head cap only after `headComplete`, and `truncate` clipped every row to the terminal
width. A collapsed `read` result emitted no body rows at all; a `bash` result emitted
its head plus a `... (N more lines) ...` note and a truncated tail.

## Fix

Added a fourth preview mode, `PreviewFull`, that renders every stored row verbatim
without head/tail caps, line truncation, or blank-line elision:

- `internal/tools/registry.go`: `PreviewFull` value.
- `internal/terminal/live/tool_render.go`: `processLine` short-circuits for
  `PreviewFull`, emitting one settled row per line, including blank lines; `consumeUTF8`
  expands tabs to content-relative eight-column stops and appends runes past the 4 KiB line
  buffer instead of dropping them; `truncate` is a no-op. Visible rows are tracked separately
  for the reconstructed-write summary fallback.
- `internal/app/history_render.go`: the full paged view sets
  `presentation.PreviewMode = coretools.PreviewFull` when there is no skipped/refused
  marker. Brief replay and live tool streaming are unchanged.

## Tests

- `TestShowHistoryShowsCompleteReadResult` — a collapsed `read` result is now shown in
  the full history view.
- `TestShowHistoryShowsCompleteToolResultWithoutPreviewLimits` — a `bash` result keeps
  its middle lines and its long line, with no `... (N more lines) ...` note.
- `TestShowHistoryPreservesBlankToolResultRows` and
  `TestShowHistoryPreservesToolResultTabAlignment` — blank rows and aligned tabular output
  survive the pager rendering path.
- `TestShowHistoryFallsBackToSummaryWhenReconstructedWriteHasNoVisibleRows` — a
  whitespace-only reconstructed write still falls back to its summary.

`make lint test-race` is green.