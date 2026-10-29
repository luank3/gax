# Ctrl-O history full tool-result rendering

> Status: complete 2026-10-28.

**Goal:** `ctrl-o` (`view the conversation in $PAGER`) shows the complete stored tool
results, not the live-preview modes that were used while the tools ran.

**Root cause:** `internal/app/history_render.go:159` set the preview mode only for the
brief replay (`historyBrief` → `PreviewCollapsed`). The full paged view left the mode
at `registry.Presentation(call)`'s default — `PreviewCollapsed` for `read` and
`PreviewHeadTail` for `bash`/`web_*`/`task_wait` — so a `read` result was collapsed to
a breadcrumb and any other result was capped to a head/tail preview that dropped middle
lines and truncated long lines.

**Fix:** added `PreviewFull` (`internal/tools/registry.go`), which renders every stored
row verbatim without head/tail caps, line truncation, or blank-line elision
(`internal/terminal/live/tool_render.go`), and set it for the full paged view
(`internal/app/history_render.go`). Brief replay and live tool streaming are unchanged.

**Tests:** `TestShowHistoryShowsCompleteReadResult`,
`TestShowHistoryShowsCompleteToolResultWithoutPreviewLimits`, and the existing
`TestShowHistoryFallsBackToSummaryWhenReconstructedWriteHasNoVisibleRows` (blank lines
are skipped so the fallback path is unchanged).

**Verification:** `make lint test-race` green.