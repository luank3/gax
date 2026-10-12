# markdown.Stream spec

## Scope

`Stream` turns provider text chunks, a width, and a styled flag into bytes for a terminal writer;
`Render` is the same engine fed one document and closed, and is the reference every partition of a
stream must reproduce. Line classification (`ScanLine`), block dispatch, inline styles, GFM tables,
and width wrapping live here. Boundary: this package writes bytes, not screen state; the live
presentation around the text (retry markers, status footers, tool rows) is `internal/terminal/live`.

## Invariants

### Streaming output must equal the whole-document render

- **Rule:** Output written across any partition of the input into `Feed` chunks plus `Close` must
  equal `Render(input, width, styled)` byte for byte.
- **Source:** `internal/terminal/markdown/stream.go:141` `Render`.
- **Assertion:** `TestStreamOutputMatchesRenderAcrossChunkPartitions` in
  `internal/terminal/markdown/stream_test.go:324`, splitting every fixture at every byte offset.
- **Rejected:** Resolving each chunk's tail as end of input: the fixture at
  `internal/terminal/markdown/stream_test.go:402` would emit the markers literally, not bold `hi`.

---
### Feed must write only bytes no later byte can change

- **Rule:** Hold any inline construct whose meaning depends on bytes that have not arrived, and
  return from `Feed` after writing just the settled prefix.
- **Source:** `internal/terminal/markdown/inline.go:18` `renderResumable` (remainder kept in
  `p.unsettled` at `internal/terminal/markdown/parser.go:1041`).
- **Assertion:** `TestStreamBoundaryResolvesSyntaxAndLeavesStreamReusable` at
  `internal/terminal/markdown/stream_test.go:472`: after `Feed("**first")` the writer holds nothing.
- **Rejected:** Writing the marker literally and un-writing it later; emitted bytes cannot be
  retracted.

---
### Copy a deferred end-of-input tail literally inside the open run

- **Rule:** Copy bytes resolved as literal at end of input verbatim and without inline parsing,
  inside whatever run is still open, and close that run only after them.
- **Source:** `internal/terminal/markdown/render.go:15` `inlineLiteralTail`, handled at
  `internal/terminal/markdown/inline.go:43`.
- **Assertion:** `TestRenderKeepsDeferredTailInsideOpenRun` at
  `internal/terminal/markdown/scan_test.go:712`: `"===- **b\u2014\r\r\n*"` keeps `*` inside the bold
  run, with `\x1b[22m` after it.
- **Rejected:** Inline-parsing the tail opens a second run for that `*` and leaves it unclosed.

---
### Let the following line decide a paragraph line's boundary

- **Rule:** Treat two or more trailing spaces, counting through a trailing `\r` and only when the
  line leaves no inline code open, as a hard break; otherwise replace the newline with one space and
  drop the next line's leading spaces and tabs.
- **Source:** `internal/terminal/markdown/parser.go:512` `paragraphBoundary`.
- **Assertion:** `TestRenderSoftJoinsProseAndPreservesHardBreaks` at
  `internal/terminal/markdown/scan_test.go:272`.
- **Rejected:** Trimming each line before the join: the two trailing spaces are the break, and that
  test pins `"foo  \nbar"` as unchanged.

---
### Write settled prose before the next chunk arrives

- **Rule:** Write prose bytes as they settle, holding back only a trailing run of whitespace or
  backticks, and a line that is still only whitespace and backticks.
- **Source:** `internal/terminal/markdown/parser.go:187` `proseHoldStart`, used by `feedProseLine`.
- **Assertion:** `TestStreamEmitsPlainProseBeforeClose` at
  `internal/terminal/markdown/stream_test.go:10`: `Feed("alpha ")` writes `alpha`, not `alpha `.
- **Rejected:** Buffering a line until its newline arrives: that test sees `alpha` before one does.

---
### Buffer a table attempt until a line disqualifies itself

- **Rule:** While a table attempt is open, write nothing, and report it through `InTable`, including
  a leading-pipe line still waiting for its delimiter row.
- **Source:** `internal/terminal/markdown/stream.go:107` `InTable`.
- **Assertion:** `TestStreamBuffersTablesUntilTheirBoundary` at
  `internal/terminal/markdown/stream_test.go:154`: output stays empty through the header, delimiter,
  and first body row.
- **Rejected:** Writing the header as it arrives: column widths are the maximum over all rows, so
  the bytes would be wrong and cannot be retracted.

---
### Render an out-of-bounds table attempt as literal lines

- **Rule:** Make a table attempt literal, never truncated, once it passes 32 columns, 2048 collected
  rows, or 65536 buffered bytes.
- **Source:** `internal/terminal/markdown/render.go:323` `parseTable`, caps declared at
  `internal/terminal/markdown/render.go:27`.
- **Assertion:** `TestRenderLeavesOversizedUnterminatedTableRowLiteral` at
  `internal/terminal/markdown/scan_test.go:701`: `Render` returns its input unchanged.
- **Rejected:** Dropping the offending row and formatting the rest: the header and body bytes the
  test compares against the input would be lost.

---
### Retract a partial word instead of splitting it

- **Rule:** When a rune does not fit and an earlier space in the row allows a break, erase back over
  the written bytes with `\x1b[<n>D\x1b[K`, write the newline, and re-emit them; keep a word with no
  earlier break intact.
- **Source:** `internal/terminal/markdown/wrap.go:221` `breakAtLastSpace`, guarded by
  `wrapper.lastBreakByte >= 0` at `internal/terminal/markdown/wrap.go:165`.
- **Assertion:** `TestRenderMatchesCursorWrapFixtures` at
  `internal/terminal/markdown/scan_test.go:375`: `Render("héllo wörld", 8, false)` is
  `"héllo wö\x1b[3D\x1b[K\nwörld"`.
- **Rejected:** Breaking at the budget without cursor control: the moved bytes stay visible and are
  written twice.

---
### Open emphasis only at a word boundary

- **Rule:** Open a `*` or `_` run only when the byte to its left is not ASCII alphanumeric and the
  byte to its right is not a space, tab, newline, or carriage return.
- **Source:** `internal/terminal/markdown/render.go:165` `canOpenEmphasis`.
- **Assertion:** `TestRenderKeepsIntrawordEmphasisMarkersLiteral` at
  `internal/terminal/markdown/scan_test.go:555`: `compile_commands.json symlink` and
  `compile**name**rest` render unchanged.
- **Rejected:** Opening at every marker, as CommonMark allows inside a word: both inputs would lose
  their markers.

## Gaps

- `render.go` has no `render_test.go`; it is covered from `scan_test.go`, `stream_test.go`, and
  `parser_test.go`, always through `Render` and `Stream`.
- `TestStreamSegmentBoundaryAfterHeadingKnownGap`, named at
  `internal/terminal/markdown/stream_test.go:586` as the boundary case that does not compose, does
  not exist in this package.
- The `modeTableStart` arm of `InTable` (`internal/terminal/markdown/stream.go:111`) has no
  asserting test; `internal/terminal/markdown/stream_test.go:154` covers the collecting arm only.

## Review

- The end-of-input arms are the highest-risk code: `stepFinalLine` and `stepFinalProseLine` at
  `internal/terminal/markdown/parser.go:293` and `internal/terminal/markdown/parser.go:448` order
  four fallbacks, and only `internal/terminal/markdown/scan_test.go:437` and
  `internal/terminal/markdown/scan_test.go:712` pin which arm wins.
- The line boundary counts as whitespace for emphasis (`internal/terminal/markdown/render.go:170`),
  pinned only by `TestRenderTreatsSoftBoundaryAsWhitespaceForEmphasis` at
  `internal/terminal/markdown/scan_test.go:152`.
- Reset's `discard` (`internal/terminal/markdown/parser.go:150`) must leave the terminal unstyled;
  `internal/terminal/live/render.go:533` writes `[unexpected end]` and calls `Reset` right after.
