# input.Editor spec

## Scope

`Editor.ReadLine` turns terminal bytes from a `ByteSource` into one submitted line, an exit gesture
(`submitted=false`), or a reader error, repainting through an `io.Writer` as the buffer changes; an
empty line submits only when `EmptySubmit` is set. `Core` owns the buffer, cursor, and history, and
`HistoryFile` loads and appends the on-disk history. Out of scope: raw mode, bracketed-paste
enable/disable, termios restore, and answer rendering.

## Invariants

### Classify escape bytes before acting on them

- **Rule:** Consume every byte of a CSI, SS3, or Meta-prefixed escape after the caller's `ESC` and
  return `ActionNone` for a form the switch does not recognise; outside the editor, confirm a
  pending `ESC` only when the next byte is neither `[` nor `O` or when the escape timeout expires.
- **Source:** `internal/terminal/input/escape.go:31` `DecodeEscape`;
  `internal/terminal/input/interrupt.go:19` `Feed` and `:51` `Timeout`.
- **Assertion:** `TestDecodeEscapeMapsCursorEditingAndPasteSequences` in
  `internal/terminal/input/core_test.go:173` (`11~` at `:200` is `ActionNone`, `3~` at `:188` is
  `ActionDeleteForward`); `TestEscapeClassifier` in
  `internal/terminal/input/interrupt_linux_test.go:7` and
  `TestEscapeClassifierTimeoutConfirmsBareEscapeOnce` at `:39`.
- **Rejected:** Guessing from the first sequence byte or the raw `0x1b`: `11~` would return the `1`
  arm's `ActionLineStart`, and `\x1b[A` would pause a running turn.

---

### Clear the draft on the first Ctrl-C and exit on a second

- **Rule:** With a non-empty buffer, store the text as session-only history and clear it, arming the
  exit gesture whose ghost text is `ctrl+c again to exit`; return `submitted=false` from an empty
  buffer only once that gesture is armed.
- **Source:** `internal/terminal/input/editor.go:173` (case `0x03`, `AddSession` at
  `internal/terminal/input/history_file.go:59`); ghost text at `internal/terminal/input/editor.go:427`.
- **Assertion:** `TestEditorEOFAndDoubleCtrlC` in `internal/terminal/input/editor_test.go:367`: the
  `double ctrl-c` subtest feeds `"\x03\x03"` and requires `line == ""`, `submitted == false`.
- **Rejected:** Exiting on a Ctrl-C that has a draft, dropping text before it is recalled.

---

### Submit on Enter and at EOF, sanitizing what is returned

- **Rule:** On Enter with an empty buffer do nothing unless `EmptySubmit` is set; on `io.EOF` with a
  non-empty buffer return `strings.ToValidUTF8(text, "\uFFFD")` as a submitted line, and with an
  empty buffer return `submitted=false`.
- **Source:** `internal/terminal/input/editor.go:227` (case `0x0d`); `internal/terminal/input/editor.go:125`.
- **Assertion:** `TestEditorAllowsEmptySubmitOnlyWhenEnabled` in
  `internal/terminal/input/editor_test.go:78` (`default` wants `submitted=false`, `resume` `true`),
  `TestEditorEOFSubmitsNonemptyDraft` at `:410`,
  `TestEditorMalformedUTF8DoesNotConsumeFollowingControlKey` at `:399`.
- **Rejected:** Returning the raw buffer at EOF instead of `"\uFFFD"` (`internal/terminal/input/editor_test.go:405`).

---

### Terminate a bracketed paste at the marker and normalize its body

- **Rule:** Read until `\x1b[201~`, turning `\r` into `\n` while swallowing the `\n` after it and
  replacing each `0x00` with a space, stop collecting past `maxPasteBytes`, then insert through
  `PasteFilter`, falling back to `PasteHook` when the body is empty.
- **Source:** `internal/terminal/input/editor.go:600` `readPasteBody`, used by `internal/terminal/input/editor.go:577`
  `readPaste`; `internal/terminal/input/core.go:70` `CommitPaste`.
- **Assertion:** `TestEditorBracketedPasteNormalizesNewlinesAndNUL` in
  `internal/terminal/input/editor_test.go:242` (`one\r\ntwo\x00` yields `one\ntwo `),
  `TestEditorEmptyBracketedPasteCallsPasteHook` at `:213`,
  `TestEditorBracketedPasteFiltersFileURIs` at `:202`.
- **Rejected:** Inserting the raw body: `\r` stays a control byte that `decodeRenderGlyph`
  (`internal/terminal/input/render.go:222`) paints as `?`.

---

### Re-derive the painted cursor row when the terminal reflows

- **Rule:** Poll `terminalSize` before applying each key; on a column change recompute the physical
  rows from the top of the painted area to the cursor, reset `paintedRows` to 0; on a rows-only
  change clamp `paintedCursorRow` to `rows-1`, keeping `paintedRows`.
- **Source:** `internal/terminal/input/resize.go:3` `resize`, rows-only arm at `internal/terminal/input/resize.go:20`,
  polling at `internal/terminal/input/editor.go:552`.
- **Assertion:** `TestResizeRepositionsCursorAfterTerminalReflow` in
  `internal/terminal/input/resize_test.go:31` (10 columns to 5 moves the cursor row from 1 to 2),
  `TestResizeClampsCursorWhenRowsShrink` at `:56` (24 rows to 2 clamps the row to 1),
  `TestEditorRefreshesGeometryBeforeApplyingNextKey` at `:74`.
- **Rejected:** Zeroing `paintedRows` on a rows-only change: `internal/terminal/input/resize_test.go:69` fixes it at 3.

---

### Keep one copy of each prompt and restore the live draft

- **Rule:** Refuse an empty line, refuse a repeat of the newest entry, drop an older exact duplicate
  when appending, evict from the oldest so at most `HistoryLimit` entries remain, and restore the
  draft stashed by Ctrl-P when Ctrl-N walks past the newest entry.
- **Source:** `internal/terminal/input/core.go:304` `AddHistory` (`HistoryLimit` at `internal/terminal/input/core.go:9`),
  `internal/terminal/input/core.go:385` `HistoryPrev`, `internal/terminal/input/core.go:399` `HistoryNext`.
- **Assertion:** `TestCoreHistoryEvictsOldestAtCapacity` in
  `internal/terminal/input/core_test.go:130` (first entry `e1`, last `e1000`),
  `TestCoreHistoryDeduplicatesSearchesAndRestoresDraft` at `:95`, and
  `TestEditorUsesHistoryAndRestoresDraft` at `internal/terminal/input/editor_test.go:228`.
- **Rejected:** Skipping the dedup scan: `internal/terminal/input/core_test.go:97` requires that call to return false.

---

### Persist the newest prompt once, and only after it is submitted

- **Rule:** Encode a record escaping `\` and `\n` only, append it when the entry is new or was held
  session-only, and keep a session-only entry out of the file until `Add` sees the same line
  submitted.
- **Source:** `internal/terminal/input/core.go:421` `EncodeHistory`;
  `internal/terminal/input/history_file.go:44` `Add` and `internal/terminal/input/history_file.go:59` `AddSession`.
- **Assertion:** `TestHistoryEncodingEscapesOnlyBackslashAndNewline` in
  `internal/terminal/input/core_test.go:159`, whose `:168` case leaves `a\xb` unchanged, and
  `TestHistoryFileSessionOnlyEntryPersistsWhenResubmitted` at
  `internal/terminal/input/history_file_test.go:50`.
- **Rejected:** Quoting records `%q`-style: `internal/terminal/input/core_test.go:168` requires the bytes `\x` to survive.

## Gaps

- The cited plan, `docs/superpowers/plans/2026-10-09-cli-input.md`, records option parsing and
  prompt joining only; no plan mentions this package's key map, resize, or history file.
- The `exitArmed` reset for a non-Ctrl-C key (`internal/terminal/input/editor.go:143`) is untested;
  `TestEditorEOFAndDoubleCtrlC` (`internal/terminal/input/editor_test.go:367`) feeds adjacent presses only.
- The pause/abort split in `confirmEscape` (`internal/terminal/input/interrupt_linux.go:86`) has no
  asserting test: `TestEscapeClassifier` scores `Feed` return values, never the callbacks.
- `maxPasteBytes` (`internal/terminal/input/editor.go:638`) has no asserting test; the largest
  pasted body in `editor_test.go` is the 11-byte `one\r\ntwo\x00`.
- The 64-byte overflow guard in `readEscapeSequence` (`internal/terminal/input/escape.go:119`) is
  untested; no `core_test.go` case is longer than `\x1b[1;5;2D`.

## Review

- The resize assertions are the only in-package tests here (`package input`, `internal/terminal/input/resize_test.go:1`), so
  they read unexported paint state; `previousPaintClipped` (`internal/terminal/input/resize.go:32`) is never entered.
- The partial paste marker dropped at `internal/terminal/input/editor.go:644` is the one place paste
  bytes vanish silently; no test drives a paste timeout or a truncated `\x1b[201~`.
- The `pending` pushback at `internal/terminal/input/editor.go:462` is the only lookahead path that
  re-enters the key loop; `internal/terminal/input/editor_test.go:399` pins one case of it.
- `Ctrl-W` (`internal/terminal/input/core.go:177` `KillWordBack`) and Meta-DEL (`internal/terminal/input/core.go:185` `KillWordBackAlnum`) split on
  different byte classes (`wordByte` counts every byte `>= 0x80`); `internal/terminal/input/core_test.go:67` pins both.
